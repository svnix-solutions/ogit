package config

import (
	altsrc "github.com/urfave/cli-altsrc/v3"
	altsrcyaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func SMTPFlags(cfg *Config) []cli.Flag {
	str := func(name, env, usage, value string, dst *string) cli.Flag {
		return &cli.StringFlag{
			Name: name, Usage: usage, Value: value, Destination: dst,
			Sources: cli.NewValueSourceChain(cli.EnvVar(env), altsrcyaml.YAML(name, altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		}
	}
	return []cli.Flag{
		str("smtp.host", "SMTP_HOST", "Outgoing mail server, required for email verification", "", &cfg.SMTP.Host),
		&cli.IntFlag{
			Name: "smtp.port", Usage: "Mail server port", Value: 587, Destination: &cfg.SMTP.Port,
			Sources: cli.NewValueSourceChain(cli.EnvVar("SMTP_PORT"), altsrcyaml.YAML("smtp.port", altsrc.NewStringPtrSourcer(&cfg.ConfigFile))),
		},
		str("smtp.username", "SMTP_USERNAME", "Mail server username (optional)", "", &cfg.SMTP.Username),
		str("smtp.password", "SMTP_PASSWORD", "Mail server password (optional)", "", &cfg.SMTP.Password),
		str("smtp.from", "SMTP_FROM", "Sender address, e.g. oGit <git@example.com>", "", &cfg.SMTP.From),
		str("smtp.tls", "SMTP_TLS", "starttls, tls (implicit TLS, usually port 465) or none", "starttls", &cfg.SMTP.TLS),
	}
}
