// Package report provides admin analytics: capacity, occupancy and revenue.
// Aggregation happens in SQL (single round-trip, GROUPING SETS) rather than in Go.
package report

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

const dateLayout = "2006-01-02"

type ShowtimeStat struct {
	ShowtimeID   int64     `json:"showtime_id"`
	MovieTitle   string    `json:"movie_title"`
	HallName     string    `json:"hall_name"`
	StartsAt     time.Time `json:"starts_at"`
	Status       string    `json:"status"`
	Capacity     int64     `json:"capacity"`
	Sold         int64     `json:"seats_sold"`
	Available    int64     `json:"seats_available"`
	OccupancyPct float64   `json:"occupancy_pct"`
	RevenueCents int64     `json:"revenue_cents"`
}

type Bucket struct {
	MovieID      *int64  `json:"movie_id,omitempty"`
	MovieTitle   *string `json:"movie_title,omitempty"`
	Day          *string `json:"day,omitempty"`
	Showtimes    int64   `json:"showtimes"`
	Capacity     int64   `json:"capacity"`
	Sold         int64   `json:"seats_sold"`
	OccupancyPct float64 `json:"occupancy_pct"`
	RevenueCents int64   `json:"revenue_cents"`
}

type Summary struct {
	From                  string   `json:"from"`
	To                    string   `json:"to"`
	Timezone              string   `json:"timezone"`
	Totals                Bucket   `json:"totals"`
	CancelledReservations int64    `json:"cancelled_reservations"`
	ByMovie               []Bucket `json:"by_movie"`
	ByDay                 []Bucket `json:"by_day"`
}

type Handler struct {
	db  *pgxpool.Pool
	loc *time.Location
}

func NewHandler(db *pgxpool.Pool, loc *time.Location) *Handler { return &Handler{db: db, loc: loc} }

func occupancy(sold, capacity int64) float64 {
	if capacity == 0 {
		return 0
	}
	return math.Round(float64(sold)*10000/float64(capacity)) / 100
}

// parseRange reads ?from=YYYY-MM-DD&to=YYYY-MM-DD (inclusive) in the theater timezone.
func (h *Handler) parseRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().In(h.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, h.loc)
	from, to := today.AddDate(0, 0, -30), today.AddDate(0, 0, 31)
	q := r.URL.Query()
	if v := q.Get("from"); v != "" {
		t, err := time.ParseInLocation(dateLayout, v, h.loc)
		if err != nil {
			return from, to, apperr.Invalid("from must be YYYY-MM-DD")
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.ParseInLocation(dateLayout, v, h.loc)
		if err != nil {
			return from, to, apperr.Invalid("to must be YYYY-MM-DD")
		}
		to = t.AddDate(0, 0, 1)
	}
	if !to.After(from) {
		return from, to, apperr.Invalid("to must not be before from")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return from, to, apperr.Invalid("range must be at most one year")
	}
	return from, to, nil
}

// Showtimes: GET /admin/reports/showtimes — capacity, occupancy and revenue per showtime.
func (h *Handler) Showtimes(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.parseRange(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rows, err := h.db.Query(r.Context(), `
SELECT s.id, m.title, h.name, s.starts_at, s.status::text, h.capacity::bigint,
       COUNT(rs.seat_id), COALESCE(SUM(rs.price_cents), 0)::bigint
FROM showtimes s
JOIN movies m ON m.id = s.movie_id
JOIN halls  h ON h.id = s.hall_id
LEFT JOIN reservation_seats rs ON rs.showtime_id = s.id AND rs.active
WHERE s.starts_at >= $1 AND s.starts_at < $2
GROUP BY s.id, m.title, h.name, h.capacity
ORDER BY s.starts_at`, from, to)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	stats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ShowtimeStat, error) {
		var s ShowtimeStat
		err := row.Scan(&s.ShowtimeID, &s.MovieTitle, &s.HallName, &s.StartsAt, &s.Status, &s.Capacity, &s.Sold, &s.RevenueCents)
		s.Available = s.Capacity - s.Sold
		s.OccupancyPct = occupancy(s.Sold, s.Capacity)
		return s, err
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"from": from.Format(dateLayout), "to": to.AddDate(0, 0, -1).Format(dateLayout), "items": stats,
	})
}

// Summary: GET /admin/reports/summary — totals, per-movie and per-day breakdowns in one query.
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.parseRange(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	sum, err := h.summary(ctx, from, to)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, sum)
}

func (h *Handler) summary(ctx context.Context, from, to time.Time) (Summary, error) {
	rows, err := h.db.Query(ctx, `
WITH per_showtime AS (
    SELECT s.id, s.movie_id, h.capacity,
           to_char(s.starts_at AT TIME ZONE $3::text, 'YYYY-MM-DD') AS day,
           COUNT(rs.seat_id)                 AS sold,
           COALESCE(SUM(rs.price_cents), 0)  AS revenue
    FROM showtimes s
    JOIN halls h ON h.id = s.hall_id
    LEFT JOIN reservation_seats rs ON rs.showtime_id = s.id AND rs.active
    WHERE s.status = 'scheduled' AND s.starts_at >= $1 AND s.starts_at < $2
    GROUP BY s.id, h.capacity
)
SELECT GROUPING(p.movie_id) AS g_movie, GROUPING(p.day) AS g_day,
       p.movie_id, MAX(m.title), p.day,
       COUNT(p.id),
       COALESCE(SUM(p.capacity), 0)::bigint,
       COALESCE(SUM(p.sold), 0)::bigint,
       COALESCE(SUM(p.revenue), 0)::bigint AS revenue
FROM per_showtime p
LEFT JOIN movies m ON m.id = p.movie_id
GROUP BY GROUPING SETS ((p.movie_id), (p.day), ())
ORDER BY g_movie, g_day, p.day, revenue DESC`, from, to, h.loc.String())
	if err != nil {
		return Summary{}, err
	}
	defer rows.Close()

	s := Summary{
		From: from.Format(dateLayout), To: to.AddDate(0, 0, -1).Format(dateLayout), Timezone: h.loc.String(),
		ByMovie: []Bucket{}, ByDay: []Bucket{},
	}
	for rows.Next() {
		var gMovie, gDay int
		var b Bucket
		if err := rows.Scan(&gMovie, &gDay, &b.MovieID, &b.MovieTitle, &b.Day, &b.Showtimes, &b.Capacity, &b.Sold, &b.RevenueCents); err != nil {
			return Summary{}, err
		}
		b.OccupancyPct = occupancy(b.Sold, b.Capacity)
		switch {
		case gMovie == 0:
			b.Day = nil
			s.ByMovie = append(s.ByMovie, b)
		case gDay == 0:
			b.MovieID, b.MovieTitle = nil, nil
			s.ByDay = append(s.ByDay, b)
		default:
			b.MovieID, b.MovieTitle, b.Day = nil, nil, nil
			s.Totals = b
		}
	}
	if err := rows.Err(); err != nil {
		return Summary{}, err
	}

	err = h.db.QueryRow(ctx, `
SELECT count(*) FROM reservations r JOIN showtimes s ON s.id = r.showtime_id
WHERE r.status = 'cancelled' AND s.starts_at >= $1 AND s.starts_at < $2`, from, to).Scan(&s.CancelledReservations)
	return s, err
}
