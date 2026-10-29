// Package runner is the `ogit runner` command: register a runner with a
// registration token, then run workflows (GitHub Actions syntax, with act)
// in Docker as the server hands them out.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/urfave/cli/v3"
)

// DefaultLabels map GitHub's usual runs-on labels to images with node
// (JavaScript actions need it) and git.
var DefaultLabels = []string{
	"ubuntu-latest:docker://node:20-bookworm",
	"ubuntu-24.04:docker://node:20-bookworm",
	"ubuntu-22.04:docker://node:20-bullseye",
}

// State is the runner's file (.runner): where its server is, and its token.
type State struct {
	URL    string   `json:"url"`
	Id     string   `json:"id"`
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Labels []string `json:"labels"`
}

func NewInstance(version string) *cli.Command {
	var file, url, token, name, network, workdir, dockerHost, jobURL string
	var labels []string
	fileFlag := &cli.StringFlag{Name: "file", Value: ".runner", Usage: "the runner's state file (holds its token)", Destination: &file}
	return &cli.Command{
		Name:  "runner",
		Usage: "Run workflows (GitHub Actions syntax) for an oGit server, in Docker",
		Commands: []*cli.Command{
			{
				Name:  "register",
				Usage: "Register this runner with a registration token (from the site, group or repository settings)",
				Flags: []cli.Flag{
					fileFlag,
					&cli.StringFlag{Name: "url", Usage: "the server's URL", Required: true, Destination: &url},
					&cli.StringFlag{Name: "token", Usage: "the registration token", Required: true, Destination: &token},
					&cli.StringFlag{Name: "name", Usage: "the runner's name (default: the host name)", Destination: &name},
					&cli.StringSliceFlag{Name: "labels", Usage: "runs-on labels, as name:docker://image", Value: DefaultLabels, Destination: &labels},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if name == "" {
						name, _ = os.Hostname()
					}
					names, _ := actrunner.ParseLabels(labels)
					client := &actrunner.Client{URL: url}
					reg, err := client.Register(ctx, token, name, names, version)
					if err != nil {
						return err
					}
					st := State{URL: strings.TrimSuffix(url, "/"), Id: reg.Id, Name: reg.Name, Token: reg.Token, Labels: labels}
					data, _ := json.MarshalIndent(st, "", "  ")
					if err := os.WriteFile(file, data, 0o600); err != nil {
						return err
					}
					fmt.Fprintf(cmd.Root().Writer, "Registered %s (%s runner). Its token is in %s; start it with `ogit runner run`.\n", reg.Name, reg.Scope, file)
					return nil
				},
			},
			{
				Name:  "run",
				Usage: "Take runs from the server and run them, one at a time",
				Flags: []cli.Flag{
					fileFlag,
					&cli.StringFlag{Name: "network", Usage: "Docker network for job containers; they must reach the server's URL", Destination: &network},
					&cli.StringFlag{Name: "workdir", Usage: "where runs and the action cache live (default: a directory next to the state file)", Destination: &workdir},
					&cli.StringFlag{Name: "docker-host", Usage: "the Docker socket (default: DOCKER_HOST or the usual socket)", Destination: &dockerHost},
					&cli.StringFlag{Name: "job-url", Usage: "where job containers reach the server, if not its public URL (e.g. http://ogit:8080 on a Docker network)", Destination: &jobURL},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					data, err := os.ReadFile(file)
					if err != nil {
						return fmt.Errorf("reading %s (run `ogit runner register` first): %w", file, err)
					}
					var st State
					if err := json.Unmarshal(data, &st); err != nil {
						return err
					}
					if workdir == "" {
						workdir = filepath.Join(filepath.Dir(file), ".runner-work")
					}
					if err := os.MkdirAll(workdir, 0o755); err != nil {
						return err
					}
					names, platforms := actrunner.ParseLabels(st.Labels)
					ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
					defer stop()
					return Loop(ctx, &actrunner.Client{URL: st.URL, Token: st.Token}, names, version,
						actrunner.Options{Platforms: platforms, WorkDir: workdir, ContainerNetwork: network, DockerHost: dockerHost, JobURL: jobURL},
						cmd.Root().Writer)
				},
			},
		},
	}
}

// Loop fetches and runs tasks until ctx ends.
func Loop(ctx context.Context, client *actrunner.Client, labels []string, version string, opts actrunner.Options, out interface{ Write([]byte) (int, error) }) error {
	fmt.Fprintf(out, "Waiting for runs (labels: %s)\n", strings.Join(labels, ", "))
	backoff := time.Second
	for ctx.Err() == nil {
		task, err := client.Fetch(ctx, labels, version, 30*time.Second)
		if err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				break
			}
			fmt.Fprintf(out, "fetching runs failed: %v (retrying in %s)\n", err, backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		if task == nil {
			continue
		}
		fmt.Fprintf(out, "Run #%d of %s (%s on %s)\n", task.Number, task.Repository, task.WorkflowPath, task.Ref)
		status := actrunner.Execute(ctx, client, task, opts)
		fmt.Fprintf(out, "Run #%d of %s: %s\n", task.Number, task.Repository, status)
	}
	return nil
}
