package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func ReleaseFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.Int64Flag{
			Name:        "releases.max_asset_size",
			Usage:       "Largest release asset accepted, in bytes (uploads are streamed, never held in memory)",
			Value:       100 << 20,
			Destination: &cfg.Releases.MaxAssetSize,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("RELEASES_MAX_ASSET_SIZE"),
				altsrcyaml.YAML("releases.max_asset_size", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}
