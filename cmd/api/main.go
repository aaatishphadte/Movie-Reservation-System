// Command api runs the movie reservation HTTP service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/config"
	"github.com/dattatrayfadte/movie-reservation-system/internal/holds"
	"github.com/dattatrayfadte/movie-reservation-system/internal/movie"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/postgres"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/ratelimit"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/redisx"
	"github.com/dattatrayfadte/movie-reservation-system/internal/report"
	"github.com/dattatrayfadte/movie-reservation-system/internal/reservation"
	"github.com/dattatrayfadte/movie-reservation-system/internal/server"
	"github.com/dattatrayfadte/movie-reservation-system/internal/showtime"
	"github.com/dattatrayfadte/movie-reservation-system/internal/user"
	"github.com/dattatrayfadte/movie-reservation-system/migrations"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := postgres.Migrate(ctx, db, migrations.FS); err != nil {
		return err
	}

	rdb, err := redisx.Connect(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return err
	}
	defer rdb.Close()

	// Wiring (manual DI keeps dependencies explicit and compile-time checked).
	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTTTL)
	seatHolds := holds.New(rdb, cfg.SeatHoldTTL)

	userStore := user.NewStore(db)
	userSvc := user.NewService(userStore, tokens)
	if err := userSvc.EnsureAdmin(ctx, cfg.AdminEmail, cfg.AdminPassword); err != nil {
		return err
	}

	showtimeStore := showtime.NewStore(db)
	showtimeSvc := showtime.NewService(showtimeStore, seatHolds, rdb, cfg.Location, cfg.TurnaroundBuffer)
	reservationSvc := reservation.NewService(reservation.NewStore(db), seatHolds, cfg.CancellationCutoff)

	handler := server.NewRouter(server.Deps{
		DB:           db,
		Redis:        rdb,
		Tokens:       tokens,
		Limiter:      ratelimit.New(rdb),
		Users:        user.NewHandler(userSvc, userStore),
		Movies:       movie.NewHandler(movie.NewStore(db)),
		Showtimes:    showtime.NewHandler(showtimeSvc, showtimeStore),
		Reservations: reservation.NewHandler(reservationSvc),
		Reports:      report.NewHandler(db, cfg.Location),
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down: draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
