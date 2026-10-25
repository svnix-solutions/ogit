package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

// RateLimitFlags configures rate limits; see config.RateLimit.
func RateLimitFlags(cfg *Config) []cli.Flag {
	rule := func(key, env, value, usage string, dest *string) cli.Flag {
		return &cli.StringFlag{
			Name: "ratelimit." + key, Value: value, Destination: dest, Usage: usage + ` ("N/window", e.g. "10/15m"; "0" turns it off)`,
			Sources: cli.NewValueSourceChain(cli.EnvVar("RATELIMIT_"+env), altsrcyaml.YAML("ratelimit."+key, altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		}
	}
	return []cli.Flag{
		&cli.BoolFlag{
			Name: "ratelimit.enabled", Value: true, Destination: &cfg.RateLimit.Enabled, Usage: "Enable rate limiting",
			Sources: cli.NewValueSourceChain(cli.EnvVar("RATELIMIT_ENABLED"), altsrcyaml.YAML("ratelimit.enabled", altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		},
		&cli.StringFlag{
			Name: "ratelimit.store", Value: "memory", Destination: &cfg.RateLimit.Store, Usage: "memory (per server) or redis (shared by every server)",
			Sources: cli.NewValueSourceChain(cli.EnvVar("RATELIMIT_STORE"), altsrcyaml.YAML("ratelimit.store", altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		},
		&cli.StringFlag{
			Name: "ratelimit.redis_url", Destination: &cfg.RateLimit.RedisURL, Usage: "Redis URL when store is redis, e.g. redis://localhost:6379/1",
			Sources: cli.NewValueSourceChain(cli.EnvVar("RATELIMIT_REDIS_URL"), altsrcyaml.YAML("ratelimit.redis_url", altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		},
		rule("login_ip", "LOGIN_IP", "10/15m", "Failed logins, password checks and git token checks per IP", &cfg.RateLimit.LoginIP),
		rule("login_account", "LOGIN_ACCOUNT", "50/15m", "Failed logins per account (any IP)", &cfg.RateLimit.LoginAccount),
		rule("register_ip", "REGISTER_IP", "5/1h", "Registrations and verification emails per IP", &cfg.RateLimit.RegisterIP),
		rule("api_user", "API_USER", "600/1m", "API requests per signed-in user", &cfg.RateLimit.APIUser),
		rule("api_anonymous", "API_ANONYMOUS", "120/1m", "API requests per IP without a session", &cfg.RateLimit.APIAnonymous),
		rule("git_user", "GIT_USER", "300/1m", "Git operations (HTTP and SSH) per user or deploy key", &cfg.RateLimit.GitUser),
		rule("git_anonymous", "GIT_ANONYMOUS", "60/1m", "Git operations per IP without credentials", &cfg.RateLimit.GitAnonymous),
	}
}
