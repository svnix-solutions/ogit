package config

import (
	"time"

	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func MirrorFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.StringSliceFlag{
			Name:        "mirrors.allowed_hosts",
			Usage:       "Private hosts, IPs or CIDRs repository mirrors and imports may fetch from (e.g. an internal git server); others resolving to private addresses are refused",
			Value:       []string{},
			Destination: &cfg.Mirrors.AllowedHosts,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("MIRRORS_ALLOWED_HOSTS"),
				altsrcyaml.YAML("mirrors.allowed_hosts", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.DurationFlag{
			Name:        "mirrors.min_interval",
			Usage:       "Shortest sync interval a pull mirror may have",
			Value:       10 * time.Minute,
			Destination: &cfg.Mirrors.MinInterval,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("MIRRORS_MIN_INTERVAL"),
				altsrcyaml.YAML("mirrors.min_interval", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.DurationFlag{
			Name:        "mirrors.default_interval",
			Usage:       "Sync interval of new pull mirrors",
			Value:       8 * time.Hour,
			Destination: &cfg.Mirrors.DefaultInterval,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("MIRRORS_DEFAULT_INTERVAL"),
				altsrcyaml.YAML("mirrors.default_interval", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}
