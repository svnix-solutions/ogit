package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRule(t *testing.T) {
	for in, want := range map[string]Rule{
		"10/15m": {10, 15 * time.Minute}, "600/1m": {600, time.Minute}, "5/1h": {5, time.Hour}, "0": {}, "": {},
	} {
		got, err := ParseRule(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"10", "x/1m", "10/abc", "10/500ms", "-1/1m"} {
		_, err := ParseRule(bad)
		assert.Error(t, err, bad)
	}
	assert.True(t, Rule{}.Off())
}

func limiter(now *time.Time) *Limiter {
	l := New(NewMemory(), zerolog.Nop())
	l.now = func() time.Time { return *now }
	return l
}

func TestAllowAndWindows(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 10, 0, time.UTC)
	l := limiter(&now)
	rule := Rule{Limit: 3, Window: time.Minute}
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		r := l.Allow(ctx, rule, "k")
		assert.True(t, r.Allowed, "request %d", i)
		assert.Equal(t, 3-i, r.Remaining)
	}
	r := l.Allow(ctx, rule, "k")
	assert.False(t, r.Allowed)
	assert.Equal(t, 50*time.Second, r.Reset, "until the minute ends")
	assert.True(t, l.Allow(ctx, rule, "other").Allowed, "keys are separate")

	now = now.Add(time.Minute)
	assert.True(t, l.Allow(ctx, rule, "k").Allowed, "a new window")
	assert.True(t, l.Allow(ctx, Rule{}, "k").Allowed, "an off rule allows everything")
}

func TestFailuresOnly(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	l := limiter(&now)
	rule := Rule{Limit: 2, Window: 15 * time.Minute}
	ctx := context.Background()
	for range 5 {
		assert.True(t, l.Check(ctx, rule, "ip").Allowed, "checking doesn't count")
	}
	l.Fail(ctx, rule, "ip")
	assert.True(t, l.Check(ctx, rule, "ip").Allowed)
	l.Fail(ctx, rule, "ip")
	r := l.Check(ctx, rule, "ip")
	assert.False(t, r.Allowed, "two failures used the budget")
	assert.Equal(t, 0, r.Remaining)
}

type brokenStore struct{}

func (brokenStore) Add(context.Context, string, int, time.Duration) (int, error) {
	return 0, errors.New("redis: connection refused")
}

func TestFailsOpen(t *testing.T) {
	l := New(brokenStore{}, zerolog.Nop())
	rule := Rule{Limit: 1, Window: time.Minute}
	for range 3 {
		assert.True(t, l.Allow(context.Background(), rule, "k").Allowed)
		assert.True(t, l.Check(context.Background(), rule, "k").Allowed)
	}
}

func TestMemoryExpires(t *testing.T) {
	m := NewMemory()
	n, _ := m.Add(context.Background(), "k", 1, 20*time.Millisecond)
	assert.Equal(t, 1, n)
	time.Sleep(30 * time.Millisecond)
	n, _ = m.Add(context.Background(), "k", 0, time.Minute)
	assert.Equal(t, 0, n, "expired")
}
