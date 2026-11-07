// Package showtime manages halls, scheduling and seat maps.
package showtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/postgres"
)

type Hall struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Capacity int    `json:"capacity"`
}

type Showtime struct {
	ID              int64     `json:"id"`
	MovieID         int64     `json:"movie_id"`
	MovieTitle      string    `json:"movie_title"`
	PosterURL       string    `json:"poster_url"`
	DurationMinutes int       `json:"duration_minutes"`
	HallID          int64     `json:"hall_id"`
	HallName        string    `json:"hall_name"`
	StartsAt        time.Time `json:"starts_at"`
	EndsAt          time.Time `json:"ends_at"`
	PriceCents      int       `json:"price_cents"`
	Status          string    `json:"status"`
}

type seatRow struct {
	ID     int64
	Row    string
	Number int
	Type   string
	Booked bool
}

type CreateInput struct {
	MovieID    int64     `json:"movie_id"`
	HallID     int64     `json:"hall_id"`
	StartsAt   time.Time `json:"starts_at"`
	PriceCents int       `json:"price_cents"`
}

func (in CreateInput) Validate() error {
	switch {
	case in.MovieID <= 0 || in.HallID <= 0:
		return apperr.Invalid("movie_id and hall_id are required")
	case in.StartsAt.IsZero():
		return apperr.Invalid("starts_at is required (RFC 3339)")
	case !in.StartsAt.After(time.Now()):
		return apperr.Invalid("starts_at must be in the future")
	case in.PriceCents < 0 || in.PriceCents > 1_000_000:
		return apperr.Invalid("price_cents must be between 0 and 1000000")
	}
	return nil
}

type HallInput struct {
	Name        string `json:"name"`
	Rows        int    `json:"rows"`
	SeatsPerRow int    `json:"seats_per_row"`
	PremiumRows int    `json:"premium_rows"`
}

func (in *HallInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "" || len(in.Name) > 100:
		return apperr.Invalid("name must be 1-100 characters")
	case in.Rows < 1 || in.Rows > 26:
		return apperr.Invalid("rows must be between 1 and 26")
	case in.SeatsPerRow < 1 || in.SeatsPerRow > 50:
		return apperr.Invalid("seats_per_row must be between 1 and 50")
	case in.PremiumRows < 0 || in.PremiumRows > in.Rows:
		return apperr.Invalid("premium_rows must be between 0 and rows")
	}
	return nil
}

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

const selectShowtime = `
SELECT s.id, s.movie_id, m.title, m.poster_url, m.duration_minutes, s.hall_id, h.name,
       s.starts_at, s.ends_at, s.price_cents, s.status::text
FROM showtimes s
JOIN movies m ON m.id = s.movie_id
JOIN halls  h ON h.id = s.hall_id`

func scanShowtime(row pgx.Row) (Showtime, error) {
	var st Showtime
	err := row.Scan(&st.ID, &st.MovieID, &st.MovieTitle, &st.PosterURL, &st.DurationMinutes,
		&st.HallID, &st.HallName, &st.StartsAt, &st.EndsAt, &st.PriceCents, &st.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Showtime{}, apperr.NotFound("showtime not found")
	}
	return st, err
}

func (s *Store) Get(ctx context.Context, id int64) (Showtime, error) {
	return scanShowtime(s.db.QueryRow(ctx, selectShowtime+` WHERE s.id = $1`, id))
}

// ListBetween returns scheduled showtimes starting in [from, to).
func (s *Store) ListBetween(ctx context.Context, from, to time.Time) ([]Showtime, error) {
	rows, err := s.db.Query(ctx, selectShowtime+`
WHERE s.status = 'scheduled' AND m.deleted_at IS NULL
  AND s.starts_at >= $1 AND s.starts_at < $2
ORDER BY m.title, s.movie_id, s.starts_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Showtime{}
	for rows.Next() {
		st, err := scanShowtime(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Create schedules a showtime. ends_at is derived from the movie runtime plus a turnaround
// buffer; the EXCLUDE constraint rejects any overlap in the same hall atomically, with no
// check-then-insert race.
func (s *Store) Create(ctx context.Context, in CreateInput, buffer time.Duration) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
INSERT INTO showtimes (movie_id, hall_id, starts_at, ends_at, price_cents)
SELECT m.id, $2, $3::timestamptz, $3::timestamptz + make_interval(mins => m.duration_minutes + $4::int), $5
FROM movies m
WHERE m.id = $1 AND m.deleted_at IS NULL
RETURNING id`, in.MovieID, in.HallID, in.StartsAt, int(buffer.Minutes()), in.PriceCents).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, apperr.NotFound("movie %d not found", in.MovieID)
	case postgres.IsExclusionViolation(err):
		return 0, apperr.Conflict("hall %d already has a showtime overlapping this slot (incl. %s turnaround)", in.HallID, buffer)
	case postgres.IsForeignKeyViolation(err):
		return 0, apperr.NotFound("hall %d not found", in.HallID)
	}
	return id, err
}

// Cancel cancels a future showtime and all of its reservations in one transaction,
// releasing the seats. Returns the number of reservations affected.
func (s *Store) Cancel(ctx context.Context, id int64) (int64, error) {
	var affected int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
UPDATE showtimes SET status = 'cancelled'
WHERE id = $1 AND status = 'scheduled' AND starts_at > now()`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return apperr.Conflict("showtime is already cancelled or has already started")
		}
		tag, err = tx.Exec(ctx, `
UPDATE reservations SET status = 'cancelled', cancelled_at = now()
WHERE showtime_id = $1 AND status = 'confirmed'`, id)
		if err != nil {
			return err
		}
		affected = tag.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE reservation_seats SET active = false WHERE showtime_id = $1 AND active`, id)
		return err
	})
	return affected, err
}

func (s *Store) seats(ctx context.Context, showtimeID int64) ([]seatRow, error) {
	rows, err := s.db.Query(ctx, `
SELECT se.id, se.row_label, se.seat_number, se.seat_type::text, (rs.seat_id IS NOT NULL)
FROM showtimes s
JOIN seats se ON se.hall_id = s.hall_id
LEFT JOIN reservation_seats rs
       ON rs.showtime_id = s.id AND rs.seat_id = se.id AND rs.active
WHERE s.id = $1
ORDER BY se.row_label, se.seat_number`, showtimeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (seatRow, error) {
		var r seatRow
		err := row.Scan(&r.ID, &r.Row, &r.Number, &r.Type, &r.Booked)
		return r, err
	})
}

func (s *Store) CreateHall(ctx context.Context, in HallInput) (Hall, error) {
	var h Hall
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO halls (name, capacity) VALUES ($1, $2) RETURNING id, name, capacity`,
			in.Name, in.Rows*in.SeatsPerRow).Scan(&h.ID, &h.Name, &h.Capacity)
		if postgres.IsUniqueViolation(err, "") {
			return apperr.Conflict("hall %q already exists", in.Name)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
INSERT INTO seats (hall_id, row_label, seat_number, seat_type)
SELECT $1, chr(64 + r), n,
       CASE WHEN r > $2::int - $4::int THEN 'premium'::seat_type ELSE 'standard'::seat_type END
FROM generate_series(1, $2::int) AS r, generate_series(1, $3::int) AS n`,
			h.ID, in.Rows, in.SeatsPerRow, in.PremiumRows)
		return err
	})
	return h, err
}

func (s *Store) ListHalls(ctx context.Context) ([]Hall, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, capacity FROM halls ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Hall, error) {
		var h Hall
		err := row.Scan(&h.ID, &h.Name, &h.Capacity)
		return h, err
	})
}
