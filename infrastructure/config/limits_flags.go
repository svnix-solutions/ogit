package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func GitFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.Int64Flag{
			Name:        "git.max_push_size",
			Usage:       "Largest push accepted over HTTP or SSH, in bytes (its pack; streamed, never held in memory); 0 for no limit",
			Value:       2 << 30,
			Destination: &cfg.Git.MaxPushSize,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("GIT_MAX_PUSH_SIZE"),
				altsrcyaml.YAML("git.max_push_size", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}

func LFSFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.Int64Flag{
			Name:        "lfs.max_object_size",
			Usage:       "Largest Git LFS object accepted, in bytes (uploads are streamed, never held in memory)",
			Value:       2 << 30,
			Destination: &cfg.LFS.MaxObjectSize,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("LFS_MAX_OBJECT_SIZE"),
				altsrcyaml.YAML("lfs.max_object_size", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}

func ActionsFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.IntFlag{
			Name:        "actions.artifact_retention_days",
			Usage:       "Days Actions artifacts are kept (a workflow's retention-days can only shorten it)",
			Value:       90,
			Destination: &cfg.Actions.ArtifactRetentionDays,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("ACTIONS_ARTIFACT_RETENTION_DAYS"),
				altsrcyaml.YAML("actions.artifact_retention_days", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.Int64Flag{
			Name:        "actions.max_artifact_size",
			Usage:       "Largest Actions artifact accepted, in bytes (uploads are streamed)",
			Value:       2 << 30,
			Destination: &cfg.Actions.MaxArtifactSize,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("ACTIONS_MAX_ARTIFACT_SIZE"),
				altsrcyaml.YAML("actions.max_artifact_size", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}

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
