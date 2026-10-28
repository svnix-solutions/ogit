// Package ratelimit counts requests in fixed time windows, in memory (per
// server) or in Redis (shared by every server).
//
// Limits fail open: if Redis is unreachable, requests are allowed (and the
// error is logged), so an outage of the limiter never takes the server down.
package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// Rule allows Limit events per Window; a zero Rule is off.
type Rule struct {
	Limit  int
	Window time.Duration
}

func (r Rule) Off() bool { return r.Limit <= 0 }

// ParseRule reads "N/window" ("10/15m", "600/1m", "5/1h"); "0" or "" is off.
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return Rule{}, nil
	}
	n, w, ok := strings.Cut(s, "/")
	limit, err := strconv.Atoi(n)
	if !ok || err != nil || limit < 0 {
		return Rule{}, fmt.Errorf("rate limit %q: want N/window, e.g. 10/15m", s)
	}
	window, err := time.ParseDuration(w)
	if err != nil || window < time.Second {
		return Rule{}, fmt.Errorf("rate limit %q: window must be a duration of at least 1s", s)
	}
	return Rule{Limit: limit, Window: window}, nil
}

// Store keeps counters. Keys include their window, so they expire.
type Store interface {
	// Add adds n (0 to just read) to key, which expires after ttl, and
	// returns the new count.
	Add(ctx context.Context, key string, n int, ttl time.Duration) (int, error)
}

// Result of a check.
type Result struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Duration // until the window ends
}

// Limiter applies rules to keys.
type Limiter struct {
	Store  Store
	Logger zerolog.Logger
	now    func() time.Time

	mu        sync.Mutex
	lastError time.Time
}

func New(store Store, logger zerolog.Logger) *Limiter {
	return &Limiter{Store: store, Logger: logger, now: time.Now}
}

// SetClock replaces the clock (tests: windows are aligned to the clock, so
// a test crossing a window boundary would see its counters reset).
func (l *Limiter) SetClock(now func() time.Time) { l.now = now }

func (l *Limiter) window(rule Rule, key string) (string, time.Duration) {
	now := l.now()
	start := now.Truncate(rule.Window)
	return fmt.Sprintf("%s:%d", key, start.Unix()), start.Add(rule.Window).Sub(now)
}

func (l *Limiter) count(ctx context.Context, rule Rule, key string, n int) Result {
	if rule.Off() {
		return Result{Allowed: true}
	}
	k, reset := l.window(rule, key)
	count, err := l.Store.Add(ctx, k, n, reset+time.Second)
	if err != nil {
		l.logError(err)
		return Result{Allowed: true, Limit: rule.Limit, Remaining: rule.Limit, Reset: reset}
	}
	return Result{Allowed: count <= rule.Limit, Limit: rule.Limit, Remaining: max(rule.Limit-count, 0), Reset: reset}
}

// Allow counts one event for key and says whether it is within the rule.
func (l *Limiter) Allow(ctx context.Context, rule Rule, key string) Result {
	return l.count(ctx, rule, key, 1)
}

// Check says whether key still has room, without counting anything (for
// rules that only count failures).
func (l *Limiter) Check(ctx context.Context, rule Rule, key string) Result {
	r := l.count(ctx, rule, key, 0)
	if !rule.Off() {
		r.Allowed = r.Remaining > 0
	}
	return r
}

// Fail counts one failure for key.
func (l *Limiter) Fail(ctx context.Context, rule Rule, key string) {
	l.count(ctx, rule, key, 1)
}

func (l *Limiter) logError(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.now().Sub(l.lastError) > 30*time.Second {
		l.lastError = l.now()
		l.Logger.Error().Err(err).Str("component", "ratelimit").Msg("rate limit store unavailable; allowing requests")
	}
}

// Memory is a Store for one server.
type Memory struct {
	mu       sync.Mutex
	counts   map[string]memEntry
	lastSwep time.Time
}

type memEntry struct {
	n       int
	expires time.Time
}

func NewMemory() *Memory { return &Memory{counts: map[string]memEntry{}} }

func (m *Memory) Add(_ context.Context, key string, n int, ttl time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	if now.Sub(m.lastSwep) > time.Minute {
		for k, e := range m.counts {
			if now.After(e.expires) {
				delete(m.counts, k)
			}
		}
		m.lastSwep = now
	}
	e := m.counts[key]
	if now.After(e.expires) {
		e = memEntry{expires: now.Add(ttl)}
	}
	e.n += n
	if n > 0 || e.n > 0 {
		m.counts[key] = e
	}
	return e.n, nil
}

// Redis is a Store shared by servers. Calls time out quickly.
type Redis struct {
	Client *redis.Client
}

func NewRedis(url string) (*Redis, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("ratelimit.redis_url: %w", err)
	}
	opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout, opts.MaxRetries = 250*time.Millisecond, 250*time.Millisecond, 250*time.Millisecond, -1
	return &Redis{Client: redis.NewClient(opts)}, nil
}

const redisPrefix = "ogit:rl:"

func (r *Redis) Add(ctx context.Context, key string, n int, ttl time.Duration) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	key = redisPrefix + key
	if n == 0 {
		v, err := r.Client.Get(ctx, key).Int()
		if err == redis.Nil {
			return 0, nil
		}
		return v, err
	}
	pipe := r.Client.TxPipeline()
	incr := pipe.IncrBy(ctx, key, int64(n))
	pipe.ExpireNX(ctx, key, ttl.Round(time.Second)+time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return int(incr.Val()), nil
}
