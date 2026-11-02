// Package workspaceagent is the `ogit workspace-agent` command: register an
// agent with a registration token, then run workspaces in Docker as the
// server hands them out.
package workspaceagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labbs/ogit/infrastructure/wsagent"
	"github.com/urfave/cli/v3"
)

// State is the agent's file (.workspace-agent): its server and token.
type State struct {
	URL   string `json:"url"`
	Id    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

func NewInstance(version string) *cli.Command {
	var file, url, token, name, network, dockerHost, gitURL, listen, advertise string
	var maxWorkspaces int
	fileFlag := &cli.StringFlag{Name: "file", Value: ".workspace-agent", Usage: "the agent's state file (holds its token)", Destination: &file}
	return &cli.Command{
		Name:  "workspace-agent",
		Usage: "Run workspaces for an oGit server, in Docker",
		Commands: []*cli.Command{
			{
				Name:  "register",
				Usage: "Register this agent with a registration token (from the admin API)",
				Flags: []cli.Flag{
					fileFlag,
					&cli.StringFlag{Name: "url", Usage: "the server's URL", Required: true, Destination: &url},
					&cli.StringFlag{Name: "token", Usage: "the registration token", Required: true, Destination: &token},
					&cli.StringFlag{Name: "name", Usage: "the agent's name (default: the host name)", Destination: &name},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if name == "" {
						name, _ = os.Hostname()
					}
					reg, err := (&wsagent.Client{URL: url}).Register(ctx, token, name, version)
					if err != nil {
						return err
					}
					data, _ := json.MarshalIndent(State{URL: strings.TrimSuffix(url, "/"), Id: reg.Id, Name: reg.Name, Token: reg.Token}, "", "  ")
					if err := os.WriteFile(file, data, 0o600); err != nil {
						return err
					}
					fmt.Fprintf(cmd.Root().Writer, "Registered %s. Its token is in %s; start it with `ogit workspace-agent run`.\n", reg.Name, file)
					return nil
				},
			},
			{
				Name:  "run",
				Usage: "Run the workspaces the server hands out",
				Flags: []cli.Flag{
					fileFlag,
					&cli.StringFlag{Name: "docker-host", Usage: "the Docker socket (default: DOCKER_HOST or the usual socket)", Destination: &dockerHost},
					&cli.StringFlag{Name: "network", Usage: "Docker network for workspaces; they must reach the server", Destination: &network},
					&cli.StringFlag{Name: "git-url", Usage: "where workspaces reach the server, if not the registered URL (e.g. http://ogit:8080 on a Docker network)", Destination: &gitURL},
					&cli.StringFlag{Name: "listen", Usage: "address for the server's connections (e.g. :8443); browsers reach workspaces through it", Destination: &listen},
					&cli.StringFlag{Name: "advertise-url", Usage: "where the server reaches --listen (e.g. https://10.0.0.5:8443)", Destination: &advertise},
					&cli.IntFlag{Name: "max-workspaces", Value: 10, Usage: "workspaces this agent runs at a time", Destination: &maxWorkspaces},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					data, err := os.ReadFile(file)
					if err != nil {
						return fmt.Errorf("reading %s (run `ogit workspace-agent register` first): %w", file, err)
					}
					var st State
					if err := json.Unmarshal(data, &st); err != nil {
						return err
					}
					if (listen == "") != (advertise == "") {
						return fmt.Errorf("--listen and --advertise-url go together")
					}
					if gitURL == "" {
						gitURL = st.URL
					}
					runtime, err := wsagent.NewDockerRuntime(dockerHost, gitURL, network)
					if err != nil {
						return err
					}
					ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
					defer stop()
					fmt.Fprintf(cmd.Root().Writer, "Waiting for workspaces (at most %d)\n", maxWorkspaces)
					agent := &wsagent.Agent{Client: &wsagent.Client{URL: st.URL, Token: st.Token}, Runtime: runtime, Version: version,
						MaxWorkspaces: maxWorkspaces, Wait: 30 * time.Second, Out: cmd.Root().Writer}
					if listen != "" {
						if err := agent.ServeProxy(ctx, listen, advertise); err != nil {
							return err
						}
					}
					return agent.Run(ctx)
				},
			},
		},
	}
}
