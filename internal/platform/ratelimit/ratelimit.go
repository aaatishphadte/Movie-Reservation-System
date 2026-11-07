// Package ratelimit implements a distributed fixed-window rate limiter backed by Redis,
// so limits hold across every API replica. It fails open: a Redis outage must not take
// the booking flow down with it.
package ratelimit

import (
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
	"github.com/dattatrayfadte/movie-reservation-system/internal/auth"
	"github.com/dattatrayfadte/movie-reservation-system/internal/httpx"
)

var incrScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n
`)

type Limiter struct{ rdb *redis.Client }

func New(rdb *redis.Client) *Limiter { return &Limiter{rdb: rdb} }

// Middleware limits requests per caller (user id when authenticated, else client IP).
func (l *Limiter) Middleware(name string, limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			now := time.Now()
			bucket := now.UnixNano() / int64(window)
			key := fmt.Sprintf("rl:%s:%s:%d", name, callerKey(r), bucket)

			n, err := incrScript.Run(r.Context(), l.rdb, []string{key}, window.Milliseconds()).Int()
			if err != nil {
				slog.WarnContext(r.Context(), "rate limiter unavailable, failing open", "err", err)
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(max(0, limit-n)))
			if n > limit {
				reset := time.Unix(0, (bucket+1)*int64(window))
				w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(reset).Seconds())+1))
				httpx.Error(w, r, apperr.RateLimited("too many requests, retry later"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func callerKey(r *http.Request) string {
	if p, ok := auth.FromContext(r.Context()); ok {
		return "u" + strconv.FormatInt(p.UserID, 10)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
