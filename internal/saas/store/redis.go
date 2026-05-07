package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Chuanyin1202/eighti-quant/internal/saas/config"
	"github.com/redis/go-redis/v9"
)

// ErrCacheMiss is returned by Redis when a key does not exist.
var ErrCacheMiss = errors.New("cache miss")

// Redis wraps the go-redis client with EightiQuant-specific helpers.
//
// Iron rule: Redis is cache only. No event-bus, no pub-sub for control flow.
// Use Postgres for source-of-truth and message ordering.
type Redis struct {
	client *redis.Client
}

// NewRedis dials the Redis configured in config and pings it.
func NewRedis(cfg config.RedisConfig) (*Redis, error) {
	cli := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := cli.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: ping %s: %w", cfg.Addr, err)
	}
	return &Redis{client: cli}, nil
}

// Get returns the raw bytes for key, or ErrCacheMiss if not present.
func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrCacheMiss
	}
	if err != nil {
		return nil, fmt.Errorf("redis get %q: %w", key, err)
	}
	return val, nil
}

// SetEx writes val under key with the given TTL.
func (r *Redis) SetEx(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, key, val, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %q: %w", key, err)
	}
	return nil
}

// Del removes a key. Returns nil even if the key did not exist.
func (r *Redis) Del(ctx context.Context, key string) error {
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del %q: %w", key, err)
	}
	return nil
}

// Close releases the underlying connection pool.
func (r *Redis) Close() error {
	return r.client.Close()
}
