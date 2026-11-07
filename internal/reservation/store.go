// Package reservation implements seat holds, booking and cancellation.
package reservation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/postgres"
	"github.com/dattatrayfadte/movie-reservation-system/internal/pricing"
)

type SeatLine struct {
	SeatID     int64  `json:"seat_id"`
	Row        string `json:"row"`
	Number     int    `json:"number"`
	PriceCents int    `json:"price_cents"`
}

type Reservation struct {
	ID          string     `json:"id"`
	UserID      int64      `json:"user_id"`
	ShowtimeID  int64      `json:"showtime_id"`
	MovieTitle  string     `json:"movie_title"`
	HallName    string     `json:"hall_name"`
	StartsAt    time.Time  `json:"starts_at"`
	Status      string     `json:"status"`
	TotalCents  int        `json:"total_cents"`
	Seats       []SeatLine `json:"seats"`
	CreatedAt   time.Time  `json:"created_at"`
	CancelledAt *time.Time `json:"cancelled_at,omitempty"`
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const selectReservation = `
SELECT r.id::text, r.user_id, r.showtime_id, m.title, h.name, s.starts_at, r.status::text,
       r.total_cents, r.created_at, r.cancelled_at,
       COALESCE((
         SELECT json_agg(json_build_object(
                  'seat_id', se.id, 'row', se.row_label, 'number', se.seat_number, 'price_cents', rs.price_cents)
                ORDER BY se.row_label, se.seat_number)
         FROM reservation_seats rs
         JOIN seats se ON se.id = rs.seat_id
         WHERE rs.reservation_id = r.id), '[]'::json)
FROM reservations r
JOIN showtimes s ON s.id = r.showtime_id
JOIN movies    m ON m.id = s.movie_id
JOIN halls     h ON h.id = s.hall_id`

func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	var seats []byte
	err := row.Scan(&r.ID, &r.UserID, &r.ShowtimeID, &r.MovieTitle, &r.HallName, &r.StartsAt,
		&r.Status, &r.TotalCents, &r.CreatedAt, &r.CancelledAt, &seats)
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, apperr.NotFound("reservation not found")
	}
	if err != nil {
		return Reservation{}, err
	}
	return r, json.Unmarshal(seats, &r.Seats)
}

func (s *Store) Get(ctx context.Context, id string) (Reservation, error) {
	return scanReservation(s.db.QueryRow(ctx, selectReservation+` WHERE r.id = $1::uuid`, id))
}

type ListFilter struct {
	UserID     *int64
	ShowtimeID *int64
	Status     string // confirmed | cancelled | ""
	Scope      string // upcoming | past | ""
	Limit      int
	Offset     int
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]Reservation, error) {
	rows, err := s.db.Query(ctx, selectReservation+`
WHERE ($1::bigint IS NULL OR r.user_id = $1)
  AND ($2::bigint IS NULL OR r.showtime_id = $2)
  AND ($3::text = '' OR r.status::text = $3)
  AND ($4::text = ''
       OR ($4 = 'upcoming' AND s.starts_at > now())
       OR ($4 = 'past' AND s.starts_at <= now()))
ORDER BY s.starts_at DESC, r.created_at DESC
LIMIT $5 OFFSET $6`, f.UserID, f.ShowtimeID, f.Status, f.Scope, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reservation{}
	for rows.Next() {
		r, err := scanReservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CheckBookable validates a seat selection before a hold is placed.
func (s *Store) CheckBookable(ctx context.Context, showtimeID int64, seatIDs []int64) error {
	var open bool
	var valid, booked int
	err := s.db.QueryRow(ctx, `
SELECT (s.status = 'scheduled' AND s.starts_at > now()),
       (SELECT count(*) FROM seats se WHERE se.hall_id = s.hall_id AND se.id = ANY($2::bigint[])),
       (SELECT count(*) FROM reservation_seats rs
         WHERE rs.showtime_id = s.id AND rs.active AND rs.seat_id = ANY($2::bigint[]))
FROM showtimes s WHERE s.id = $1`, showtimeID, seatIDs).Scan(&open, &valid, &booked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return apperr.NotFound("showtime not found")
	case err != nil:
		return err
	case !open:
		return apperr.Conflict("showtime is not open for booking")
	case valid != len(seatIDs):
		return apperr.Invalid("one or more seats do not belong to this showtime's hall")
	case booked > 0:
		return apperr.Conflict("one or more selected seats are already booked")
	}
	return nil
}

type createParams struct {
	UserID         int64
	ShowtimeID     int64
	SeatIDs        []int64
	IdempotencyKey string
}

// Create books seats atomically. Guarantees:
//   - Idempotent: a retried request with the same Idempotency-Key returns the original booking.
//   - No overbooking: the partial unique index (showtime_id, seat_id) WHERE active makes a
//     double sale impossible even if Redis holds are bypassed or lost.
//   - The showtime row is locked FOR SHARE so it cannot be cancelled mid-booking.
//
// Returns (reservationID, replayed, error).
func (s *Store) Create(ctx context.Context, p createParams) (string, bool, error) {
	if p.IdempotencyKey != "" {
		var id string
		err := s.db.QueryRow(ctx, `SELECT id::text FROM reservations WHERE user_id = $1 AND idempotency_key = $2`,
			p.UserID, p.IdempotencyKey).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var hallID int64
	var base int
	err = tx.QueryRow(ctx, `
SELECT hall_id, price_cents FROM showtimes
WHERE id = $1 AND status = 'scheduled' AND starts_at > now()
FOR SHARE`, p.ShowtimeID).Scan(&hallID, &base)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, apperr.Conflict("showtime is not open for booking")
	}
	if err != nil {
		return "", false, err
	}

	var idem *string
	if p.IdempotencyKey != "" {
		idem = &p.IdempotencyKey
	}
	var id string
	err = tx.QueryRow(ctx, `
INSERT INTO reservations (user_id, showtime_id, idempotency_key) VALUES ($1, $2, $3)
ON CONFLICT (user_id, idempotency_key) DO NOTHING
RETURNING id::text`, p.UserID, p.ShowtimeID, idem).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent request with the same key won the race; return its result.
		_ = tx.Rollback(ctx)
		err = s.db.QueryRow(ctx, `SELECT id::text FROM reservations WHERE user_id = $1 AND idempotency_key = $2`,
			p.UserID, p.IdempotencyKey).Scan(&id)
		return id, true, err
	}
	if err != nil {
		return "", false, err
	}

	rows, err := tx.Query(ctx, `SELECT id, seat_type::text FROM seats WHERE hall_id = $1 AND id = ANY($2::bigint[])`, hallID, p.SeatIDs)
	if err != nil {
		return "", false, err
	}
	prices := make(map[int64]int, len(p.SeatIDs))
	for rows.Next() {
		var seatID int64
		var typ string
		if err := rows.Scan(&seatID, &typ); err != nil {
			rows.Close()
			return "", false, err
		}
		prices[seatID] = pricing.SeatPrice(base, typ)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(prices) != len(p.SeatIDs) {
		return "", false, apperr.Invalid("one or more seats do not belong to this showtime's hall")
	}

	amounts := make([]int32, len(p.SeatIDs))
	total := 0
	for i, sid := range p.SeatIDs {
		amounts[i] = int32(prices[sid])
		total += prices[sid]
	}

	_, err = tx.Exec(ctx, `
INSERT INTO reservation_seats (reservation_id, showtime_id, seat_id, price_cents)
SELECT $1::uuid, $2, x.seat_id, x.price
FROM unnest($3::bigint[], $4::int[]) AS x (seat_id, price)`, id, p.ShowtimeID, p.SeatIDs, amounts)
	if postgres.IsUniqueViolation(err, "reservation_seats_no_double_booking") {
		return "", false, apperr.Conflict("one or more selected seats have just been booked by someone else")
	}
	if err != nil {
		return "", false, err
	}
	if _, err := tx.Exec(ctx, `UPDATE reservations SET total_cents = $2 WHERE id = $1::uuid`, id, total); err != nil {
		return "", false, err
	}
	return id, false, tx.Commit(ctx)
}

// Cancel cancels an upcoming reservation and frees its seats. The row lock (FOR UPDATE)
// serialises concurrent cancel attempts.
func (s *Store) Cancel(ctx context.Context, id string, actorID int64, isAdmin bool, cutoff time.Duration) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var owner int64
		var status string
		var startsAt time.Time
		err := tx.QueryRow(ctx, `
SELECT r.user_id, r.status::text, s.starts_at
FROM reservations r JOIN showtimes s ON s.id = r.showtime_id
WHERE r.id = $1::uuid
FOR UPDATE OF r`, id).Scan(&owner, &status, &startsAt)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != actorID && !isAdmin) {
			return apperr.NotFound("reservation not found") // don't reveal other users' bookings
		}
		if err != nil {
			return err
		}
		if status != "confirmed" {
			return apperr.Conflict("reservation is already %s", status)
		}
		if time.Until(startsAt) < cutoff {
			return apperr.Conflict("only upcoming reservations can be cancelled (at least %s before showtime)", cutoff)
		}
		if _, err := tx.Exec(ctx, `UPDATE reservations SET status = 'cancelled', cancelled_at = now() WHERE id = $1::uuid`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE reservation_seats SET active = false WHERE reservation_id = $1::uuid`, id)
		return err
	})
}
