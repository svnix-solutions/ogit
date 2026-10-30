//go:build integration

package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Two servers sharing Redis share their limits.
func TestRedisLimitsShared(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine@sha256:6ba19c50b56777f5c11c58eff8ddb69d0d5f27ba131cffb63f180ccfd43c66af")
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	url, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)

	newLimiter := func() *Limiter {
		store, err := NewRedis(url)
		require.NoError(t, err)
		return New(store, zerolog.Nop())
	}
	a, b := newLimiter(), newLimiter()
	rule := Rule{Limit: 3, Window: time.Hour}
	assert.True(t, a.Allow(ctx, rule, "ip").Allowed)
	assert.True(t, b.Allow(ctx, rule, "ip").Allowed)
	assert.True(t, a.Allow(ctx, rule, "ip").Allowed)
	assert.False(t, b.Allow(ctx, rule, "ip").Allowed, "the other server's requests count")
	assert.False(t, a.Check(ctx, rule, "ip").Allowed)
	assert.True(t, a.Check(ctx, rule, "fresh").Allowed, "reading a missing key")

	keys, err := a.Store.(*Redis).Client.Keys(ctx, redisPrefix+"ip:*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	ttl, err := a.Store.(*Redis).Client.TTL(ctx, keys[0]).Result()
	require.NoError(t, err)
	assert.Positive(t, ttl, "counters expire")
}
