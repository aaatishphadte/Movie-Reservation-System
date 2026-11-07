//go:build integration

package reservation_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/holds"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/postgres"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/redisx"
	"github.com/dattatrayfadte/movie-reservation-system/internal/reservation"
	"github.com/dattatrayfadte/movie-reservation-system/migrations"
)

type fixture struct {
	db         *pgxpool.Pool
	svc        *reservation.Service
	showtimeID int64
	seatIDs    []int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	dbURL := envOr("TEST_DATABASE_URL", "postgres://movies:movies@localhost:5432/movies?sslmode=disable")
	db, err := postgres.Connect(ctx, dbURL)
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	t.Cleanup(db.Close)
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	rdb, err := redisx.Connect(ctx, envOr("TEST_REDIS_ADDR", "localhost:6379"), "", 0)
	if err != nil {
		t.Skipf("redis unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })

	// A showtime far in the future at a random slot so repeated runs never overlap.
	start := time.Now().Add(time.Duration(400+rand.IntN(50_000)) * 24 * time.Hour).Truncate(time.Hour)
	var movieID, showtimeID, hallID int64
	must(t, db.QueryRow(ctx, `INSERT INTO movies (title, duration_minutes) VALUES ('Race Test', 90) RETURNING id`).Scan(&movieID))
	must(t, db.QueryRow(ctx, `SELECT id FROM halls WHERE name = 'Hall 2'`).Scan(&hallID))
	must(t, db.QueryRow(ctx, `INSERT INTO showtimes (movie_id, hall_id, starts_at, ends_at, price_cents)
		VALUES ($1, $2, $3::timestamptz, $3::timestamptz + interval '2 hours', 1000) RETURNING id`, movieID, hallID, start).Scan(&showtimeID))
	t.Cleanup(func() {
		// Remove test data so the demo database isn't polluted.
		ctx := context.Background()
		_, _ = db.Exec(ctx, `DELETE FROM reservations WHERE showtime_id = $1`, showtimeID)
		_, _ = db.Exec(ctx, `DELETE FROM reservation_seats WHERE showtime_id = $1`, showtimeID)
		_, _ = db.Exec(ctx, `DELETE FROM showtimes WHERE id = $1`, showtimeID)
		_, _ = db.Exec(ctx, `DELETE FROM movies WHERE id = $1`, movieID)
		_, _ = db.Exec(ctx, `DELETE FROM users WHERE email LIKE 'racer-%@test.local'`)
	})

	rows, err := db.Query(ctx, `SELECT id FROM seats WHERE hall_id = $1 ORDER BY id LIMIT 3`, hallID)
	must(t, err)
	var seats []int64
	for rows.Next() {
		var id int64
		must(t, rows.Scan(&id))
		seats = append(seats, id)
	}

	svc := reservation.NewService(reservation.NewStore(db), holds.New(rdb, time.Minute), 30*time.Minute)
	return fixture{db: db, svc: svc, showtimeID: showtimeID, seatIDs: seats}
}

func newUser(t *testing.T, db *pgxpool.Pool) auth.Principal {
	t.Helper()
	var id int64
	email := fmt.Sprintf("racer-%d-%d@test.local", time.Now().UnixNano(), rand.Int())
	must(t, db.QueryRow(context.Background(),
		`INSERT INTO users (email, name, password_hash) VALUES ($1, 'racer', 'x') RETURNING id`, email).Scan(&id))
	return auth.Principal{UserID: id, Role: auth.RoleUser}
}

// TestNoOverbookingUnderContention fires N concurrent customers at the same seat.
// Exactly one booking must succeed; every other attempt must be a clean conflict.
func TestNoOverbookingUnderContention(t *testing.T) {
	f := setup(t)
	const customers = 50
	users := make([]auth.Principal, customers)
	for i := range users {
		users[i] = newUser(t, f.db)
	}

	var ok, conflicts atomic.Int32
	var wg sync.WaitGroup
	startGun := make(chan struct{})
	for _, u := range users {
		wg.Add(1)
		go func(u auth.Principal) {
			defer wg.Done()
			<-startGun
			_, _, err := f.svc.Reserve(context.Background(), u,
				reservation.ReserveInput{ShowtimeID: f.showtimeID, SeatIDs: []int64{f.seatIDs[0], f.seatIDs[1]}}, "")
			switch {
			case err == nil:
				ok.Add(1)
			case apperr.Is(err, apperr.KindConflict):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(u)
	}
	close(startGun)
	wg.Wait()

	if ok.Load() != 1 || conflicts.Load() != customers-1 {
		t.Fatalf("want 1 success and %d conflicts, got %d / %d", customers-1, ok.Load(), conflicts.Load())
	}
	var sold int
	must(t, f.db.QueryRow(context.Background(),
		`SELECT count(*) FROM reservation_seats WHERE showtime_id = $1 AND active`, f.showtimeID).Scan(&sold))
	if sold != 2 {
		t.Fatalf("want exactly 2 active seat sales, got %d", sold)
	}
}

func TestIdempotentRetryAndCancelReleasesSeat(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	u := newUser(t, f.db)
	in := reservation.ReserveInput{ShowtimeID: f.showtimeID, SeatIDs: []int64{f.seatIDs[2]}}

	first, replayed, err := f.svc.Reserve(ctx, u, in, "key-1")
	must(t, err)
	if replayed {
		t.Fatal("first call must not be a replay")
	}
	second, replayed, err := f.svc.Reserve(ctx, u, in, "key-1")
	must(t, err)
	if !replayed || second.ID != first.ID {
		t.Fatalf("retry must replay original reservation: %v %s vs %s", replayed, second.ID, first.ID)
	}

	other := newUser(t, f.db)
	if _, _, err := f.svc.Reserve(ctx, other, in, ""); !apperr.Is(err, apperr.KindConflict) {
		t.Fatalf("seat must be taken, got %v", err)
	}
	if _, err := f.svc.Cancel(ctx, other, first.ID); !apperr.Is(err, apperr.KindNotFound) {
		t.Fatalf("users must not cancel others' reservations, got %v", err)
	}
	cancelled, err := f.svc.Cancel(ctx, u, first.ID)
	must(t, err)
	if cancelled.Status != "cancelled" {
		t.Fatalf("status = %s", cancelled.Status)
	}
	if _, _, err := f.svc.Reserve(ctx, other, in, ""); err != nil {
		t.Fatalf("seat must be bookable again after cancellation: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
