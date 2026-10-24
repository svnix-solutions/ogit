package cache

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryLRU(t *testing.T) {
	ctx := context.Background()
	m := NewMemory(10)
	m.Set(ctx, "a", []byte("aaaa"))
	m.Set(ctx, "b", []byte("bbbb"))
	_, ok := m.Get(ctx, "a") // a is now most recently used
	require.True(t, ok)

	m.Set(ctx, "c", []byte("cccc")) // over budget: evicts b, the least recently used
	_, ok = m.Get(ctx, "b")
	assert.False(t, ok, "least recently used is evicted")
	v, ok := m.Get(ctx, "a")
	assert.True(t, ok)
	assert.Equal(t, "aaaa", string(v))
	assert.LessOrEqual(t, m.Size(), int64(10))

	m.Set(ctx, "huge", make([]byte, 11))
	_, ok = m.Get(ctx, "huge")
	assert.False(t, ok, "values larger than the budget are not cached")
	_, ok = m.Get(ctx, "a")
	assert.True(t, ok, "and do not flush the cache")
}

func TestNew(t *testing.T) {
	c, err := New(Config{}, zerolog.Nop())
	require.NoError(t, err)
	assert.IsType(t, &Memory{}, c, "memory is the default")
	c, err = New(Config{Type: "none"}, zerolog.Nop())
	require.NoError(t, err)
	c.Set(context.Background(), "k", []byte("v"))
	_, ok := c.Get(context.Background(), "k")
	assert.False(t, ok)
	_, err = New(Config{Type: "redis"}, zerolog.Nop())
	assert.ErrorContains(t, err, "redis_url")
	_, err = New(Config{Type: "disk"}, zerolog.Nop())
	assert.Error(t, err)
}

// An unreachable Redis behaves like a miss, quickly: it must never make
// reads slower than going to S3 directly.
func TestRedisUnavailableIsAFastMiss(t *testing.T) {
	c, err := New(Config{Type: "redis", RedisURL: "redis://127.0.0.1:1/0"}, zerolog.Nop())
	require.NoError(t, err)

	start := time.Now()
	c.Set(context.Background(), "k", []byte("v"))
	_, ok := c.Get(context.Background(), "k")
	assert.False(t, ok)
	assert.Less(t, time.Since(start), time.Second, "first failure is bounded by the short timeouts")

	start = time.Now()
	for i := 0; i < 100; i++ {
		c.Get(context.Background(), "k")
	}
	assert.Less(t, time.Since(start), 50*time.Millisecond, "then Redis is skipped instead of retried")
}
