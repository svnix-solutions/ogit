package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func SecurityFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name: "security.encryption_key",
			Usage: "Key encrypting secrets stored in the database (authenticator app seeds), at least 32 bytes. " +
				"Keep it: changing it turns off app-based two-factor authentication for everyone (recovery codes still work). Unset: no app-based 2FA.",
			Destination: &cfg.Security.EncryptionKey,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("SECURITY_ENCRYPTION_KEY"),
				altsrcyaml.YAML("security.encryption_key", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.BoolFlag{
			Name:        "security.require_2fa",
			Usage:       "Require two-factor authentication from every user (when false, site admins can still require it from the admin page)",
			Destination: &cfg.Security.RequireTwoFactor,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("SECURITY_REQUIRE_2FA"),
				altsrcyaml.YAML("security.require_2fa", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}

func SessionFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:        "session.expiration_minutes",
			Value:       60 * 24 * 30, // 1 month
			Destination: &cfg.Session.ExpirationMinutes,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("SESSION_EXPIRATION_MINUTES"),
				altsrcyaml.YAML("session.expiration_minutes", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.StringFlag{
			Name:        "session.secret_key",
			Usage:       "HMAC key for session tokens, at least 32 bytes. If unset, a random key is generated at startup and sessions do not survive restarts.",
			Destination: &cfg.Session.SecretKey,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("SESSION_SECRET_KEY"),
				altsrcyaml.YAML("session.secret_key", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.StringFlag{
			Name:        "session.issuer",
			Value:       "ogit", // Issuer name for the session tokens
			Destination: &cfg.Session.Issuer,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("SESSION_ISSUER"),
				altsrcyaml.YAML("session.issuer", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}
