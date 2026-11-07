// Package redisx creates the Redis client with startup retries.
package redisx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

func Connect(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
		PoolSize:     50,
	})
	for attempt := 1; ; attempt++ {
		err := rdb.Ping(ctx).Err()
		if err == nil {
			return rdb, nil
		}
		if attempt == 15 {
			_ = rdb.Close()
			return nil, fmt.Errorf("redis not reachable: %w", err)
		}
		slog.Warn("waiting for redis", "attempt", attempt, "err", err)
		select {
		case <-ctx.Done():
			_ = rdb.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
