// Package server assembles the HTTP router. All routes are declared here so the API surface
// and its authorization rules can be reviewed in one place.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/dattatrayfadte/movie-reservation-system/api"
	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
	"github.com/dattatrayfadte/movie-reservation-system/internal/movie"
	"github.com/dattatrayfadte/movie-reservation-system/internal/platform/ratelimit"
	"github.com/dattatrayfadte/movie-reservation-system/internal/report"
	"github.com/dattatrayfadte/movie-reservation-system/internal/reservation"
	"github.com/dattatrayfadte/movie-reservation-system/internal/showtime"
	"github.com/dattatrayfadte/movie-reservation-system/internal/user"
)

type Deps struct {
	DB           *pgxpool.Pool
	Redis        *redis.Client
	Tokens       *auth.TokenManager
	Limiter      *ratelimit.Limiter
	Users        *user.Handler
	Movies       *movie.Handler
	Showtimes    *showtime.Handler
	Reservations *reservation.Handler
	Reports      *report.Handler
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, requestLogger, middleware.Recoverer,
		middleware.Timeout(15*time.Second))

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, r, apperr.NotFound("no route for %s %s; see GET / for the API index", r.Method, r.URL.Path))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": r.Method + " is not allowed on " + r.URL.Path},
		})
	})
	r.Get("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(api.DocsPage)
	})
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(api.OpenAPISpec)
	})
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		// Browsers land on the interactive docs; API clients get a JSON index.
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.Redirect(w, r, "/docs", http.StatusFound)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"service": "movie-reservation-system",
			"version": "v1",
			"links": map[string]string{
				"docs":      "/docs",
				"openapi":   "/openapi.yaml",
				"health":    "/healthz",
				"readiness": "/readyz",
				"genres":    "/api/v1/genres",
				"movies":    "/api/v1/movies",
				"halls":     "/api/v1/halls",
				"schedule":  "/api/v1/showtimes?date=YYYY-MM-DD",
				"seat_map":  "/api/v1/showtimes/{id}/seats",
				"signup":    "POST /api/v1/auth/signup",
				"login":     "POST /api/v1/auth/login",
			},
		})
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", readiness(d))

	authLimit := d.Limiter.Middleware("auth", 10, time.Minute)
	bookingLimit := d.Limiter.Middleware("booking", 30, time.Minute)

	r.Route("/api/v1", func(r chi.Router) {
		// Public
		r.With(authLimit).Post("/auth/signup", d.Users.Signup)
		r.With(authLimit).Post("/auth/login", d.Users.Login)
		r.Get("/genres", d.Movies.ListGenres)
		r.Get("/movies", d.Movies.List)
		r.Get("/movies/{id}", d.Movies.Get)
		r.Get("/halls", d.Showtimes.ListHalls)
		r.Get("/showtimes", d.Showtimes.Schedule)
		r.Get("/showtimes/{id}", d.Showtimes.Get)
		r.With(auth.OptionalAuthenticate(d.Tokens)).Get("/showtimes/{id}/seats", d.Showtimes.SeatMap)

		// Authenticated users
		r.Group(func(r chi.Router) {
			r.Use(auth.Authenticate(d.Tokens))
			r.Get("/me", d.Users.Me)

			r.With(bookingLimit).Post("/showtimes/{id}/holds", d.Reservations.Hold)
			r.Post("/showtimes/{id}/holds/release", d.Reservations.ReleaseHold)
			r.With(bookingLimit).Post("/reservations", d.Reservations.Create)
			r.Get("/reservations", d.Reservations.ListMine)
			r.Get("/reservations/{id}", d.Reservations.Get)
			r.Post("/reservations/{id}/cancel", d.Reservations.Cancel)

			// Admin only
			r.Route("/admin", func(r chi.Router) {
				r.Use(auth.RequireRole(auth.RoleAdmin))
				r.Get("/users", d.Users.List)
				r.Patch("/users/{id}/role", d.Users.SetRole)

				r.Post("/movies", d.Movies.Create)
				r.Put("/movies/{id}", d.Movies.Update)
				r.Delete("/movies/{id}", d.Movies.Delete)

				r.Post("/halls", d.Showtimes.CreateHall)
				r.Post("/showtimes", d.Showtimes.Create)
				r.Post("/showtimes/{id}/cancel", d.Showtimes.Cancel)

				r.Get("/reservations", d.Reservations.ListAll)
				r.Get("/reports/showtimes", d.Reports.Showtimes)
				r.Get("/reports/summary", d.Reports.Summary)
			})
		})
	})
	return r
}

func readiness(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		checks := map[string]string{"postgres": "ok", "redis": "ok"}
		status := http.StatusOK
		if err := d.DB.Ping(ctx); err != nil {
			checks["postgres"], status = err.Error(), http.StatusServiceUnavailable
		}
		if err := d.Redis.Ping(ctx).Err(); err != nil {
			checks["redis"], status = err.Error(), http.StatusServiceUnavailable
		}
		httpx.JSON(w, status, checks)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.LogAttrs(r.Context(), slog.LevelInfo, "http_request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", ww.Status()),
			slog.Int("bytes", ww.BytesWritten()),
			slog.Duration("duration", time.Since(start)),
			slog.String("request_id", middleware.GetReqID(r.Context())),
			slog.String("remote_ip", r.RemoteAddr),
		)
	})
}
