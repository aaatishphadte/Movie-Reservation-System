// Package config loads 12-factor configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr    string
	LogLevel    slog.Level
	DatabaseURL string

	RedisAddr     string
	RedisPassword string
	RedisDB       int

	JWTSecret string
	JWTTTL    time.Duration

	SeatHoldTTL        time.Duration // how long a customer keeps selected seats
	CancellationCutoff time.Duration // no cancellations closer than this to showtime start
	TurnaroundBuffer   time.Duration // cleaning time between shows in a hall
	Location           *time.Location

	AdminEmail    string
	AdminPassword string
}

func Load() (Config, error) {
	var errs []error
	c := Config{
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		DatabaseURL:   env("DATABASE_URL", "postgres://movies:movies@localhost:5432/movies?sslmode=disable"),
		RedisAddr:     env("REDIS_ADDR", "localhost:6379"),
		RedisPassword: env("REDIS_PASSWORD", ""),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		AdminEmail:    os.Getenv("ADMIN_EMAIL"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
	}
	c.RedisDB = envInt("REDIS_DB", 0, &errs)
	c.JWTTTL = envDuration("JWT_TTL", 24*time.Hour, &errs)
	c.SeatHoldTTL = envDuration("SEAT_HOLD_TTL", 5*time.Minute, &errs)
	c.CancellationCutoff = envDuration("CANCELLATION_CUTOFF", 30*time.Minute, &errs)
	c.TurnaroundBuffer = envDuration("TURNAROUND_BUFFER", 15*time.Minute, &errs)

	if err := c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	loc, err := time.LoadLocation(env("THEATER_TIMEZONE", "UTC"))
	if err != nil {
		errs = append(errs, fmt.Errorf("THEATER_TIMEZONE: %w", err))
	}
	c.Location = loc

	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be set and at least 32 characters"))
	}
	if (c.AdminEmail == "") != (c.AdminPassword == "") {
		errs = append(errs, errors.New("ADMIN_EMAIL and ADMIN_PASSWORD must be set together"))
	}
	return c, errors.Join(errs...)
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, errs *[]error) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
	}
	return n
}

func envDuration(key string, def time.Duration, errs *[]error) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
	}
	return d
}
