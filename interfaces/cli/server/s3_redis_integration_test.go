//go:build integration

package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Two servers on one bucket share a Redis cache: once one has served a
// repository, the other reads its packs from Redis, not S3.
func TestS3RedisCacheSharedBetweenServers(t *testing.T) {
	ctx := context.Background()
	redisCtr, err := tcredis.Run(ctx, "redis:7-alpine")
	testcontainers.CleanupContainer(t, redisCtr)
	require.NoError(t, err)
	redisURL, err := redisCtr.ConnectionString(ctx)
	require.NoError(t, err)

	ep, counter := s3test.StartCountingFake(t)
	withRedis := func(c *config.Config) {
		s3test.Configure(c, ep)
		c.Storage.S3.Cache.Type = "redis"
		c.Storage.S3.Cache.RedisURL = redisURL
	}
	a := startTestServer(t, withRedis)
	owner := a.newUser(t, "owner")
	a.createRepo(t, owner.session, "Shared", false)
	b := startTestServer(t, withRedis, func(c *config.Config) { c.Database = a.deps.Config.Database })

	git := newGitCLI(t, newSSHKey(t, "x"), 0)                               // public repo over HTTP: no key used
	git.mustRun(t.TempDir(), "clone", "-q", a.url+"/owner/shared.git", ".") // server A warms Redis

	counter.Reset()
	status, _ := b.get(t, "/api/health", "")
	require.Equal(t, http.StatusOK, status)
	git.mustRun(t.TempDir(), "clone", "-q", b.url+"/owner/shared.git", ".")
	assert.Zero(t, counter.Get("RANGE"), "server B reads pack blocks from Redis: %v", counter)
}
