package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func SSHFlags(cfg *Config) []cli.Flag {
	src := func(name, env string) cli.ValueSourceChain {
		return cli.NewValueSourceChain(cli.EnvVar(env), altsrcyaml.YAML(name, altsrc.NewStringPtrSourcer(&cfg.ConfigFile)))
	}
	return []cli.Flag{
		&cli.BoolFlag{Name: "ssh.enabled", Usage: "Serve git over SSH", Value: false, Destination: &cfg.SSH.Enabled, Sources: src("ssh.enabled", "SSH_ENABLED")},
		&cli.IntFlag{Name: "ssh.port", Usage: "SSH port", Value: 2222, Destination: &cfg.SSH.Port, Sources: src("ssh.port", "SSH_PORT")},
		&cli.StringFlag{
			Name: "ssh.host_key_path", Usage: "ed25519 host key file, generated on first start if missing (keep it: clients warn when it changes)",
			Value: "./ssh_host_ed25519_key", Destination: &cfg.SSH.HostKeyPath, Sources: src("ssh.host_key_path", "SSH_HOST_KEY_PATH"),
		},
	}
}
