package showtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/holds"
	"github.com/dattatrayfadte/movie-reservation-system/internal/pricing"
)

const (
	dateLayout  = "2006-01-02"
	scheduleTTL = 60 * time.Second
)

type Service struct {
	store  *Store
	holds  *holds.Store
	cache  *redis.Client
	loc    *time.Location
	buffer time.Duration
}

func NewService(store *Store, h *holds.Store, cache *redis.Client, loc *time.Location, buffer time.Duration) *Service {
	return &Service{store: store, holds: h, cache: cache, loc: loc, buffer: buffer}
}

type Slot struct {
	ID         int64     `json:"id"`
	HallID     int64     `json:"hall_id"`
	HallName   string    `json:"hall_name"`
	StartsAt   time.Time `json:"starts_at"`
	EndsAt     time.Time `json:"ends_at"`
	PriceCents int       `json:"price_cents"`
}

type MovieSchedule struct {
	MovieID         int64  `json:"movie_id"`
	Title           string `json:"title"`
	PosterURL       string `json:"poster_url"`
	DurationMinutes int    `json:"duration_minutes"`
	Showtimes       []Slot `json:"showtimes"`
}

type DaySchedule struct {
	Date     string          `json:"date"`
	Timezone string          `json:"timezone"`
	Movies   []MovieSchedule `json:"movies"`
}

func scheduleKey(date string) string { return "cache:schedule:" + date }

// Schedule returns all movies with their showtimes for a calendar day in the theater's
// timezone. It is the hottest read path, so it is cached in Redis (read-through, short TTL,
// explicitly invalidated on writes). Cache failures degrade to the database.
func (s *Service) Schedule(ctx context.Context, date string) (DaySchedule, error) {
	if date == "" {
		date = time.Now().In(s.loc).Format(dateLayout)
	}
	day, err := time.ParseInLocation(dateLayout, date, s.loc)
	if err != nil {
		return DaySchedule{}, apperr.Invalid("date must be YYYY-MM-DD")
	}

	if b, err := s.cache.Get(ctx, scheduleKey(date)).Bytes(); err == nil {
		var ds DaySchedule
		if json.Unmarshal(b, &ds) == nil {
			return ds, nil
		}
	} else if !errors.Is(err, redis.Nil) {
		slog.WarnContext(ctx, "schedule cache read failed", "err", err)
	}

	// AddDate (not +24h) keeps DST-transition days correct.
	list, err := s.store.ListBetween(ctx, day, day.AddDate(0, 0, 1))
	if err != nil {
		return DaySchedule{}, err
	}
	ds := DaySchedule{Date: date, Timezone: s.loc.String(), Movies: []MovieSchedule{}}
	for _, st := range list {
		if n := len(ds.Movies); n == 0 || ds.Movies[n-1].MovieID != st.MovieID {
			ds.Movies = append(ds.Movies, MovieSchedule{
				MovieID: st.MovieID, Title: st.MovieTitle, PosterURL: st.PosterURL, DurationMinutes: st.DurationMinutes,
			})
		}
		m := &ds.Movies[len(ds.Movies)-1]
		m.Showtimes = append(m.Showtimes, Slot{
			ID: st.ID, HallID: st.HallID, HallName: st.HallName, StartsAt: st.StartsAt, EndsAt: st.EndsAt, PriceCents: st.PriceCents,
		})
	}

	if b, err := json.Marshal(ds); err == nil {
		if err := s.cache.Set(ctx, scheduleKey(date), b, scheduleTTL).Err(); err != nil {
			slog.WarnContext(ctx, "schedule cache write failed", "err", err)
		}
	}
	return ds, nil
}

func (s *Service) invalidate(ctx context.Context, t time.Time) {
	if err := s.cache.Del(ctx, scheduleKey(t.In(s.loc).Format(dateLayout))).Err(); err != nil {
		slog.WarnContext(ctx, "schedule cache invalidation failed", "err", err)
	}
}

func (s *Service) Get(ctx context.Context, id int64) (Showtime, error) { return s.store.Get(ctx, id) }

func (s *Service) Create(ctx context.Context, in CreateInput) (Showtime, error) {
	id, err := s.store.Create(ctx, in, s.buffer)
	if err != nil {
		return Showtime{}, err
	}
	s.invalidate(ctx, in.StartsAt)
	return s.store.Get(ctx, id)
}

func (s *Service) Cancel(ctx context.Context, id int64) (int64, error) {
	st, err := s.store.Get(ctx, id)
	if err != nil {
		return 0, err
	}
	n, err := s.store.Cancel(ctx, id)
	if err != nil {
		return 0, err
	}
	s.invalidate(ctx, st.StartsAt)
	return n, nil
}

const (
	SeatAvailable = "available"
	SeatHeld      = "held"
	SeatHeldByYou = "held_by_you"
	SeatBooked    = "booked"
)

type Seat struct {
	ID         int64  `json:"id"`
	Row        string `json:"row"`
	Number     int    `json:"number"`
	Type       string `json:"type"`
	PriceCents int    `json:"price_cents"`
	Status     string `json:"status"`
}

type SeatMap struct {
	Showtime  Showtime `json:"showtime"`
	Capacity  int      `json:"capacity"`
	Available int      `json:"available"`
	Seats     []Seat   `json:"seats"`
}

// SeatMap merges durable state (booked, from Postgres) with ephemeral state (held, from Redis).
func (s *Service) SeatMap(ctx context.Context, id int64, viewer *auth.Principal) (SeatMap, error) {
	st, err := s.store.Get(ctx, id)
	if err != nil {
		return SeatMap{}, err
	}
	rows, err := s.store.seats(ctx, id)
	if err != nil {
		return SeatMap{}, err
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	holders, err := s.holds.Holders(ctx, id, ids)
	if err != nil {
		slog.WarnContext(ctx, "seat holds unavailable; showing booked state only", "err", err)
		holders = map[int64]string{}
	}
	me := ""
	if viewer != nil {
		me = holds.Owner(viewer.UserID)
	}

	sm := SeatMap{Showtime: st, Capacity: len(rows), Seats: make([]Seat, len(rows))}
	bookable := st.Status == "scheduled" && st.StartsAt.After(time.Now())
	for i, r := range rows {
		status := SeatAvailable
		switch owner, held := holders[r.ID]; {
		case r.Booked:
			status = SeatBooked
		case held && owner == me:
			status = SeatHeldByYou
		case held:
			status = SeatHeld
		}
		if status == SeatAvailable && bookable {
			sm.Available++
		}
		sm.Seats[i] = Seat{ID: r.ID, Row: r.Row, Number: r.Number, Type: r.Type,
			PriceCents: pricing.SeatPrice(st.PriceCents, r.Type), Status: status}
	}
	return sm, nil
}
