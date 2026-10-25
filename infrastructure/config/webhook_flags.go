package config

import (
	"time"

	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func WebhookFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{
			Name:        "webhooks.allowed_hosts",
			Usage:       "Private hosts, IPs or CIDRs webhooks may reach (e.g. an internal CI server); others resolving to private addresses are refused",
			Value:       []string{},
			Destination: &cfg.Webhooks.AllowedHosts,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WEBHOOKS_ALLOWED_HOSTS"),
				altsrcyaml.YAML("webhooks.allowed_hosts", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.DurationFlag{
			Name:        "webhooks.timeout",
			Usage:       "Timeout of one webhook delivery attempt",
			Value:       10 * time.Second,
			Destination: &cfg.Webhooks.Timeout,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WEBHOOKS_TIMEOUT"),
				altsrcyaml.YAML("webhooks.timeout", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}
