// Package holds implements short-lived seat holds in Redis.
//
// A hold is a soft lock that gives a customer a few minutes to complete checkout without the
// seats being taken from under them. It protects UX and absorbs contention before it reaches
// Postgres. It is NOT the source of truth: the unique index on reservation_seats is.
//
// All seats of a request are acquired atomically by a Lua script (all-or-nothing), so two
// customers selecting overlapping seat sets can never each end up with a partial hold.
// Keys share a hash tag per showtime ({stN}) so the script is Redis Cluster compatible.
package holds

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dattatrayfadte/movie-reservation-system/internal/apperr"
)

type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

func New(rdb *redis.Client, ttl time.Duration) *Store { return &Store{rdb: rdb, ttl: ttl} }

// Owner returns the hold owner token for a user.
func Owner(userID int64) string { return "u:" + strconv.FormatInt(userID, 10) }

func keys(showtimeID int64, seatIDs []int64) []string {
	ks := make([]string, len(seatIDs))
	for i, id := range seatIDs {
		ks[i] = fmt.Sprintf("hold:{st%d}:%d", showtimeID, id)
	}
	return ks
}

// Returns 0 on success, or the 1-based index of the first seat held by someone else.
// Re-acquiring your own hold refreshes its TTL.
var acquireScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
  local holder = redis.call('GET', key)
  if holder and holder ~= ARGV[1] then
    return i
  end
end
for _, key in ipairs(KEYS) do
  redis.call('SET', key, ARGV[1], 'PX', ARGV[2])
end
return 0
`)

// Deletes only holds owned by the caller (compare-and-delete).
var releaseScript = redis.NewScript(`
local n = 0
for _, key in ipairs(KEYS) do
  if redis.call('GET', key) == ARGV[1] then
    n = n + redis.call('DEL', key)
  end
end
return n
`)

func (s *Store) Acquire(ctx context.Context, showtimeID int64, seatIDs []int64, owner string) (time.Time, error) {
	idx, err := acquireScript.Run(ctx, s.rdb, keys(showtimeID, seatIDs), owner, s.ttl.Milliseconds()).Int()
	if err != nil {
		return time.Time{}, fmt.Errorf("acquire seat holds: %w", err)
	}
	if idx > 0 {
		return time.Time{}, apperr.Conflict("seat %d is currently held by another customer", seatIDs[idx-1])
	}
	return time.Now().Add(s.ttl), nil
}

func (s *Store) Release(ctx context.Context, showtimeID int64, seatIDs []int64, owner string) error {
	return releaseScript.Run(ctx, s.rdb, keys(showtimeID, seatIDs), owner).Err()
}

// Holders returns seatID -> owner for currently held seats.
func (s *Store) Holders(ctx context.Context, showtimeID int64, seatIDs []int64) (map[int64]string, error) {
	out := make(map[int64]string)
	if len(seatIDs) == 0 {
		return out, nil
	}
	vals, err := s.rdb.MGet(ctx, keys(showtimeID, seatIDs)...).Result()
	if err != nil {
		return nil, err
	}
	for i, v := range vals {
		if owner, ok := v.(string); ok {
			out[seatIDs[i]] = owner
		}
	}
	return out, nil
}
