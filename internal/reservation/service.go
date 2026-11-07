package reservation

import (
	"context"
	"log/slog"
	"regexp"
	"time"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/holds"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

const maxSeatsPerBooking = 10

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validateSeats(ids []int64) error {
	if len(ids) == 0 || len(ids) > maxSeatsPerBooking {
		return apperr.Invalid("select between 1 and %d seats", maxSeatsPerBooking)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return apperr.Invalid("invalid seat id %d", id)
		}
		if _, dup := seen[id]; dup {
			return apperr.Invalid("duplicate seat id %d", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

type SeatsInput struct {
	SeatIDs []int64 `json:"seat_ids"`
}

func (in SeatsInput) Validate() error { return validateSeats(in.SeatIDs) }

type ReserveInput struct {
	ShowtimeID int64   `json:"showtime_id"`
	SeatIDs    []int64 `json:"seat_ids"`
}

func (in ReserveInput) Validate() error {
	if in.ShowtimeID <= 0 {
		return apperr.Invalid("showtime_id is required")
	}
	return validateSeats(in.SeatIDs)
}

type Hold struct {
	ShowtimeID int64     `json:"showtime_id"`
	SeatIDs    []int64   `json:"seat_ids"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Service struct {
	store  *Store
	holds  *holds.Store
	cutoff time.Duration
}

func NewService(store *Store, h *holds.Store, cancellationCutoff time.Duration) *Service {
	return &Service{store: store, holds: h, cutoff: cancellationCutoff}
}

// Hold temporarily reserves seats for the caller while they check out.
func (s *Service) Hold(ctx context.Context, p auth.Principal, showtimeID int64, seatIDs []int64) (Hold, error) {
	if err := s.store.CheckBookable(ctx, showtimeID, seatIDs); err != nil {
		return Hold{}, err
	}
	exp, err := s.holds.Acquire(ctx, showtimeID, seatIDs, holds.Owner(p.UserID))
	if err != nil {
		return Hold{}, err
	}
	return Hold{ShowtimeID: showtimeID, SeatIDs: seatIDs, ExpiresAt: exp}, nil
}

func (s *Service) ReleaseHold(ctx context.Context, p auth.Principal, showtimeID int64, seatIDs []int64) error {
	return s.holds.Release(ctx, showtimeID, seatIDs, holds.Owner(p.UserID))
}

// Reserve confirms a booking.
//
//  1. Re-acquire the Redis holds (idempotent for the same user). If another customer holds a
//     seat we fail fast without touching Postgres.
//  2. Commit to Postgres, where the unique index is the source of truth.
//  3. Release the holds — the seats are now durably booked (or the booking failed).
//
// If Redis is unavailable we log and continue: correctness never depends on Redis.
func (s *Service) Reserve(ctx context.Context, p auth.Principal, in ReserveInput, idempotencyKey string) (Reservation, bool, error) {
	if len(idempotencyKey) > 100 {
		return Reservation{}, false, apperr.Invalid("Idempotency-Key must be at most 100 characters")
	}
	owner := holds.Owner(p.UserID)
	if _, err := s.holds.Acquire(ctx, in.ShowtimeID, in.SeatIDs, owner); err != nil {
		if apperr.Is(err, apperr.KindConflict) {
			return Reservation{}, false, err
		}
		slog.WarnContext(ctx, "seat hold layer unavailable; relying on database constraints", "err", err)
	}
	defer func() {
		if err := s.holds.Release(context.WithoutCancel(ctx), in.ShowtimeID, in.SeatIDs, owner); err != nil {
			slog.WarnContext(ctx, "release seat holds", "err", err)
		}
	}()

	id, replayed, err := s.store.Create(ctx, createParams{
		UserID: p.UserID, ShowtimeID: in.ShowtimeID, SeatIDs: in.SeatIDs, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return Reservation{}, false, err
	}
	r, err := s.store.Get(ctx, id)
	return r, replayed, err
}

func (s *Service) Get(ctx context.Context, p auth.Principal, id string) (Reservation, error) {
	if !uuidRe.MatchString(id) {
		return Reservation{}, apperr.NotFound("reservation not found")
	}
	r, err := s.store.Get(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	if r.UserID != p.UserID && !p.IsAdmin() {
		return Reservation{}, apperr.NotFound("reservation not found")
	}
	return r, nil
}

func (s *Service) Cancel(ctx context.Context, p auth.Principal, id string) (Reservation, error) {
	if !uuidRe.MatchString(id) {
		return Reservation{}, apperr.NotFound("reservation not found")
	}
	if err := s.store.Cancel(ctx, id, p.UserID, p.IsAdmin(), s.cutoff); err != nil {
		return Reservation{}, err
	}
	return s.store.Get(ctx, id)
}

func (s *Service) ListMine(ctx context.Context, p auth.Principal, scope string, page httpx.Page) ([]Reservation, error) {
	if scope != "" && scope != "upcoming" && scope != "past" {
		return nil, apperr.Invalid("scope must be 'upcoming' or 'past'")
	}
	uid := p.UserID
	return s.store.List(ctx, ListFilter{UserID: &uid, Scope: scope, Limit: page.Limit, Offset: page.Offset})
}

func (s *Service) ListAll(ctx context.Context, f ListFilter) ([]Reservation, error) {
	if f.Status != "" && f.Status != "confirmed" && f.Status != "cancelled" {
		return nil, apperr.Invalid("status must be 'confirmed' or 'cancelled'")
	}
	if f.Scope != "" && f.Scope != "upcoming" && f.Scope != "past" {
		return nil, apperr.Invalid("scope must be 'upcoming' or 'past'")
	}
	return s.store.List(ctx, f)
}
