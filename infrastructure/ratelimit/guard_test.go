package ratelimit

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clientIP(t *testing.T, trusted []string, xff string) string {
	t.Helper()
	var cfg config.Config
	cfg.Server.TrustedProxies = trusted
	g, err := NewGuard(cfg, zerolog.Nop())
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString(g.ClientIP(c)) })
	req := httptest.NewRequest("GET", "/", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := app.Test(req) // the connection comes from 0.0.0.0
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestClientIP(t *testing.T) {
	assert.Equal(t, "0.0.0.0", clientIP(t, nil, "1.2.3.4"), "no trusted proxies: the header is ignored")
	assert.Equal(t, "5.6.7.8", clientIP(t, []string{"0.0.0.0"}, "1.2.3.4, 5.6.7.8"), "the address the proxy added, not what the client sent")
	assert.Equal(t, "1.2.3.4", clientIP(t, []string{"0.0.0.0", "10.0.0.0/8"}, "1.2.3.4, 10.1.2.3"), "other trusted hops are skipped")
	assert.Equal(t, "0.0.0.0", clientIP(t, []string{"0.0.0.0"}, "garbage"), "an unparseable header")
}

func TestGuardConfig(t *testing.T) {
	var cfg config.Config
	cfg.RateLimit.Enabled = true
	cfg.RateLimit.LoginIP = "10/15m"
	g, err := NewGuard(cfg, zerolog.Nop())
	require.NoError(t, err)
	assert.Equal(t, 10, g.Rules.LoginIP.Limit)
	assert.True(t, g.Rules.APIUser.Off(), "empty is off")

	cfg.RateLimit.Enabled = false
	g, err = NewGuard(cfg, zerolog.Nop())
	require.NoError(t, err)
	assert.True(t, g.Rules.LoginIP.Off(), "disabled: every rule is off")

	for _, mutate := range []func(*config.Config){
		func(c *config.Config) { c.RateLimit.Enabled = true; c.RateLimit.GitUser = "lots" },
		func(c *config.Config) { c.RateLimit.Store = "memcached" },
		func(c *config.Config) { c.RateLimit.Store = "redis" },
		func(c *config.Config) { c.Server.TrustedProxies = []string{"proxy.local"} },
	} {
		var bad config.Config
		mutate(&bad)
		_, err := NewGuard(bad, zerolog.Nop())
		assert.Error(t, err)
	}
}
