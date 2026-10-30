//go:build integration

package cache

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func TestRedisCache(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine@sha256:6ba19c50b56777f5c11c58eff8ddb69d0d5f27ba131cffb63f180ccfd43c66af")
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	url, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)

	c, err := New(Config{Type: "redis", RedisURL: url}, zerolog.Nop())
	require.NoError(t, err)
	_, ok := c.Get(ctx, "pack-block")
	assert.False(t, ok)
	c.Set(ctx, "pack-block", []byte{0, 1, 2, 255})
	v, ok := c.Get(ctx, "pack-block")
	require.True(t, ok)
	assert.Equal(t, []byte{0, 1, 2, 255}, v, "binary values survive")
}
