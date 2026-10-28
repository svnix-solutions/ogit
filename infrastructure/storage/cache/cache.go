// Package cache caches immutable byte blobs (S3 pack blocks and indexes).
// Keys must name immutable content: entries are never invalidated, only
// evicted, so the cache is safe to share between servers.
package cache

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Cache stores immutable blobs. Implementations never fail a request: an
// unavailable backend behaves like a miss.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte)
}

// Config selects the backend: "memory" (default), "redis" or "none".
type Config struct {
	Type     string
	MaxMB    int    // memory: budget in MiB
	RedisURL string // redis: e.g. redis://localhost:6379/0
}

// New builds the configured cache.
func New(cfg Config, logger zerolog.Logger) (Cache, error) {
	switch cfg.Type {
	case "", "memory":
		max := cfg.MaxMB
		if max <= 0 {
			max = 256
		}
		return NewMemory(int64(max) << 20), nil
	case "none":
		return None{}, nil
	case "redis":
		if cfg.RedisURL == "" {
			return nil, fmt.Errorf("storage.s3.cache.redis_url is required for the redis cache")
		}
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("storage.s3.cache.redis_url: %w", err)
		}
		// A cache must be faster than S3 or be skipped: short timeouts, no
		// retries, and a pause after errors (see Redis.available).
		opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout = 250*time.Millisecond, 250*time.Millisecond, 250*time.Millisecond
		opts.MaxRetries = -1
		return &Redis{Client: redis.NewClient(opts), Logger: logger, TTL: 7 * 24 * time.Hour}, nil
	default:
		return nil, fmt.Errorf("storage.s3.cache.type must be memory, redis or none, got %q", cfg.Type)
	}
}

// None caches nothing.
type None struct{}

func (None) Get(context.Context, string) ([]byte, bool) { return nil, false }
func (None) Set(context.Context, string, []byte)        {}

// Memory is an in-process LRU bounded by the total size of its values.
type Memory struct {
	mu      sync.Mutex
	max     int64
	size    int64
	order   *list.List // front = most recently used
	entries map[string]*list.Element
}

type memEntry struct {
	key   string
	value []byte
}

func NewMemory(maxBytes int64) *Memory {
	return &Memory{max: maxBytes, order: list.New(), entries: map[string]*list.Element{}}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	m.order.MoveToFront(el)
	return el.Value.(*memEntry).value, true
}

func (m *Memory) Set(_ context.Context, key string, value []byte) {
	if int64(len(value)) > m.max {
		return // larger than the whole budget
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[key]; ok {
		m.order.MoveToFront(el)
		return // immutable: same key, same value
	}
	m.entries[key] = m.order.PushFront(&memEntry{key: key, value: value})
	m.size += int64(len(value))
	for m.size > m.max {
		oldest := m.order.Back()
		e := oldest.Value.(*memEntry)
		m.order.Remove(oldest)
		delete(m.entries, e.key)
		m.size -= int64(len(e.value))
	}
}

// Size returns the bytes currently held.
func (m *Memory) Size() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.size
}

// Redis caches in Redis (or Valkey). Eviction is Redis's job: configure
// maxmemory with an allkeys-lru policy; TTL is a safety net.
type Redis struct {
	Client *redis.Client
	Logger zerolog.Logger
	TTL    time.Duration

	mu        sync.Mutex
	skipUntil time.Time // after an error, go straight to S3 until then
}

const (
	keyPrefix  = "ogit:blob:"
	errorPause = 30 * time.Second
)

func (r *Redis) available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Now().After(r.skipUntil)
}

func (r *Redis) failed(err error, op string) {
	r.mu.Lock()
	r.skipUntil = time.Now().Add(errorPause)
	r.mu.Unlock()
	r.Logger.Warn().Err(err).Str("component", "storage.cache").Str("op", op).
		Msgf("redis unavailable, using S3 directly for %s", errorPause)
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	if !r.available() {
		return nil, false
	}
	v, err := r.Client.Get(ctx, keyPrefix+key).Bytes()
	if err != nil {
		if err != redis.Nil {
			r.failed(err, "get")
		}
		return nil, false
	}
	return v, true
}

func (r *Redis) Set(ctx context.Context, key string, value []byte) {
	if !r.available() {
		return
	}
	if err := r.Client.Set(ctx, keyPrefix+key, value, r.TTL).Err(); err != nil {
		r.failed(err, "set")
	}
}
