package ratelimit

import (
	"context"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

// Rules are the configured limits (see config.RateLimit).
type Rules struct {
	LoginIP, LoginAccount, RegisterIP, APIUser, APIAnonymous, GitUser, GitAnonymous Rule
}

// Guard applies the server's rules to requests.
type Guard struct {
	*Limiter
	Rules   Rules
	trusted []*net.IPNet
}

// NewGuard reads the configuration. With rate limiting disabled every rule
// is off.
func NewGuard(cfg config.Config, logger zerolog.Logger) (*Guard, error) {
	trusted, err := parseCIDRs(cfg.Server.TrustedProxies)
	if err != nil {
		return nil, err
	}
	var store Store = NewMemory()
	switch cfg.RateLimit.Store {
	case "", "memory":
	case "redis":
		if cfg.RateLimit.RedisURL == "" {
			return nil, fmt.Errorf("ratelimit.redis_url is required when ratelimit.store is redis")
		}
		if store, err = NewRedis(cfg.RateLimit.RedisURL); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("ratelimit.store must be memory or redis, got %q", cfg.RateLimit.Store)
	}
	g := &Guard{Limiter: New(store, logger), trusted: trusted}
	if !cfg.RateLimit.Enabled {
		return g, nil
	}
	for _, r := range []struct {
		dst  *Rule
		name string
		val  string
	}{
		{&g.Rules.LoginIP, "login_ip", cfg.RateLimit.LoginIP},
		{&g.Rules.LoginAccount, "login_account", cfg.RateLimit.LoginAccount},
		{&g.Rules.RegisterIP, "register_ip", cfg.RateLimit.RegisterIP},
		{&g.Rules.APIUser, "api_user", cfg.RateLimit.APIUser},
		{&g.Rules.APIAnonymous, "api_anonymous", cfg.RateLimit.APIAnonymous},
		{&g.Rules.GitUser, "git_user", cfg.RateLimit.GitUser},
		{&g.Rules.GitAnonymous, "git_anonymous", cfg.RateLimit.GitAnonymous},
	} {
		rule, err := ParseRule(r.val)
		if err != nil {
			return nil, fmt.Errorf("ratelimit.%s: %w", r.name, err)
		}
		*r.dst = rule
	}
	return g, nil
}

// parseCIDRs validates http.trusted_proxies (IPs or CIDRs).
func parseCIDRs(entries []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			if ip := net.ParseIP(e); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				e = fmt.Sprintf("%s/%d", e, bits)
			}
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("http.trusted_proxies: %q is not an IP or CIDR", e)
		}
		out = append(out, n)
	}
	return out, nil
}

func (g *Guard) isTrusted(ip net.IP) bool {
	for _, n := range g.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP is the connection's address, or, when it comes from a trusted
// proxy, the last address in X-Forwarded-For that is not a trusted proxy.
// Reading from the right means a client can't pick its address by sending
// its own X-Forwarded-For: proxies append to it.
func (g *Guard) ClientIP(c *fiber.Ctx) string {
	remote := c.Context().RemoteIP()
	if !g.isTrusted(remote) {
		return remote.String()
	}
	hops := strings.Split(c.Get(fiber.HeaderXForwardedFor), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			break
		}
		if !g.isTrusted(ip) {
			return ip.String()
		}
	}
	return remote.String()
}

// Deny answers 429 with Retry-After, as JSON for the API.
func Deny(c *fiber.Ctx, r Result) error {
	c.Set(fiber.HeaderRetryAfter, RetryAfter(r))
	return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
		"code":    fiber.StatusTooManyRequests,
		"details": "too many requests; try again in " + RetryAfter(r) + " seconds",
		"type":    "RATE_LIMITED",
	})
}

// RetryAfter is the number of seconds until the window ends, at least 1.
func RetryAfter(r Result) string {
	return strconv.Itoa(max(1, int(math.Ceil(r.Reset.Seconds()))))
}

// SetHeaders reports the rule's state (draft RateLimit headers).
func SetHeaders(c *fiber.Ctx, r Result) {
	if r.Limit == 0 {
		return
	}
	c.Set("RateLimit-Limit", strconv.Itoa(r.Limit))
	c.Set("RateLimit-Remaining", strconv.Itoa(r.Remaining))
	c.Set("RateLimit-Reset", RetryAfter(r))
}

// LoginBlocked checks the failed-login budgets of ip and account.
func (g *Guard) LoginBlocked(ctx context.Context, ip, account string) (Result, bool) {
	if r := g.Check(ctx, g.Rules.LoginIP, "login:ip:"+ip); !r.Allowed {
		return r, true
	}
	if account != "" {
		if r := g.Check(ctx, g.Rules.LoginAccount, "login:acct:"+strings.ToLower(account)); !r.Allowed {
			return r, true
		}
	}
	return Result{Allowed: true}, false
}

// LoginFailed counts a failed login (wrong password or token).
func (g *Guard) LoginFailed(ctx context.Context, ip, account string) {
	g.Fail(ctx, g.Rules.LoginIP, "login:ip:"+ip)
	if account != "" {
		g.Fail(ctx, g.Rules.LoginAccount, "login:acct:"+strings.ToLower(account))
	}
}
