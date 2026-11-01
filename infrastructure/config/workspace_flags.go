package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func WorkspaceFlags(cfg *Config) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:        "workspaces.domain",
			Usage:       "Domain workspaces are served under, <id>.<domain>, with wildcard DNS and TLS; empty turns workspaces off",
			Destination: &cfg.Workspaces.Domain,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_DOMAIN"),
				altsrcyaml.YAML("workspaces.domain", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.IntFlag{
			Name:        "workspaces.cpus",
			Usage:       "CPUs of a workspace",
			Value:       2,
			Destination: &cfg.Workspaces.CPUs,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_CPUS"),
				altsrcyaml.YAML("workspaces.cpus", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.IntFlag{
			Name:        "workspaces.memory_gb",
			Usage:       "Memory of a workspace, in GB",
			Value:       4,
			Destination: &cfg.Workspaces.MemoryGB,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_MEMORY_GB"),
				altsrcyaml.YAML("workspaces.memory_gb", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.IntFlag{
			Name:        "workspaces.disk_gb",
			Usage:       "Disk of a workspace, in GB",
			Value:       10,
			Destination: &cfg.Workspaces.DiskGB,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_DISK_GB"),
				altsrcyaml.YAML("workspaces.disk_gb", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.DurationFlag{
			Name:        "workspaces.idle_timeout",
			Usage:       "How long a workspace may sit idle before it is stopped",
			Value:       30 * time.Minute,
			Destination: &cfg.Workspaces.IdleTimeout,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_IDLE_TIMEOUT"),
				altsrcyaml.YAML("workspaces.idle_timeout", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.DurationFlag{
			Name:        "workspaces.retention",
			Usage:       "How long an unused workspace is kept before it is deleted",
			Value:       14 * 24 * time.Hour,
			Destination: &cfg.Workspaces.Retention,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_RETENTION"),
				altsrcyaml.YAML("workspaces.retention", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
		&cli.IntFlag{
			Name:        "workspaces.max_running",
			Usage:       "Workspaces one user may run at a time",
			Value:       1,
			Destination: &cfg.Workspaces.MaxRunning,
			Sources: cli.NewValueSourceChain(
				cli.EnvVar("WORKSPACES_MAX_RUNNING"),
				altsrcyaml.YAML("workspaces.max_running", altsrc.NewStringPtrSourcer(&cfg.ConfigFile)),
			),
		},
	}
}

// CheckWorkspaceDomain refuses a workspaces.domain that isn't a bare host
// name, or that is the server's own host: code in a workspace must never
// share an origin with oGit's sessions.
func CheckWorkspaceDomain(domain, publicURL string) error {
	if domain == "" {
		return nil
	}
	if strings.ContainsAny(domain, "/:@*") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return fmt.Errorf("workspaces.domain must be a host name, such as workspaces.example.com")
	}
	if u, err := url.Parse(publicURL); err == nil && strings.EqualFold(u.Hostname(), domain) {
		return fmt.Errorf("workspaces.domain must differ from http.public_url's host")
	}
	return nil
}
