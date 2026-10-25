package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func ServerFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:        "http.port",
			Aliases:     []string{"p"},
			Value:       8080,
			Destination: &cfg.Server.Port,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("HTTP_PORT"),
				altsrcyaml.YAML("http.port", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.BoolFlag{
			Name:        "http.logs",
			Value:       false,
			Destination: &cfg.Server.HttpLogs,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("HTTP_LOGS"),
				altsrcyaml.YAML("http.logs", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.StringFlag{
			Name:        "http.public_url",
			Usage:       "Externally reachable base URL (e.g. https://git.example.com), used in links sent by email",
			Destination: &cfg.Server.PublicURL,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("HTTP_PUBLIC_URL"),
				altsrcyaml.YAML("http.public_url", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.StringSliceFlag{
			Name:        "http.trusted_proxies",
			Usage:       "IPs or CIDRs of reverse proxies whose X-Forwarded-For/-Proto headers are trusted (comma separated); none by default",
			Value:       []string{},
			Destination: &cfg.Server.TrustedProxies,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("HTTP_TRUSTED_PROXIES"),
				altsrcyaml.YAML("http.trusted_proxies", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}
