package wsagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

// DefaultNetwork is the workspaces' network when none is given: the firewall
// applies to a whole network, so they get one of their own.
const DefaultNetwork = "ogit-workspaces"

// ErrNoFirewallImage: agents have no default until the image is published.
var ErrNoFirewallImage = errors.New("no firewall image: pass --firewall-image, or --no-firewall to run without network rules")

func (d *DockerRuntime) ensureNetwork(ctx context.Context) (network.Inspect, error) {
	inspect, err := d.Docker.NetworkInspect(ctx, d.Network, network.InspectOptions{})
	if err == nil {
		return inspect, nil
	}
	if !errdefs.IsNotFound(err) {
		return inspect, err
	}
	if _, err := d.Docker.NetworkCreate(ctx, d.Network, network.CreateOptions{Driver: "bridge"}); err != nil && !errdefs.IsConflict(err) {
		return inspect, err
	}
	return d.Docker.NetworkInspect(ctx, d.Network, network.InspectOptions{})
}

// bridgeName is the host interface of a bridge network.
func bridgeName(n network.Inspect) string {
	if name := n.Options["com.docker.network.bridge.name"]; name != "" {
		return name
	}
	return "br-" + n.ID[:12]
}

// allowed lists what workspaces must still reach: the server, wherever it is,
// plus FirewallAllow. Hosts resolve here, so a host that doesn't is an error.
func (d *DockerRuntime) allowed(ctx context.Context) ([]string, error) {
	targets := append([]string{}, d.FirewallAllow...)
	u, err := url.Parse(d.GitURL)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid git URL %q", d.GitURL)
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	targets = append(targets, net.JoinHostPort(u.Hostname(), port))
	var out []string
	for _, target := range targets {
		host, port, err := net.SplitHostPort(target)
		if err != nil {
			return nil, fmt.Errorf("invalid firewall address %q: %w", target, err)
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil {
			return nil, fmt.Errorf("resolving %s: %w", host, err)
		}
		for _, ip := range ips {
			out = append(out, net.JoinHostPort(ip.String(), port))
		}
	}
	return out, nil
}

// protect installs the network's rules, before every start: Docker and
// firewall services flush them on restarts. A workspace that can't be
// protected doesn't start.
func (d *DockerRuntime) protect(ctx context.Context) error {
	if !d.NoFirewall && d.FirewallImage == "" {
		return ErrNoFirewallImage
	}
	if d.Network == "" {
		if d.NoFirewall {
			return nil
		}
		return errors.New("the firewall needs a network of its own")
	}
	n, err := d.ensureNetwork(ctx)
	if err != nil {
		return fmt.Errorf("the workspace network: %w", err)
	}
	if d.NoFirewall {
		return nil
	}
	if n.Driver != "bridge" || len(n.IPAM.Config) == 0 {
		return fmt.Errorf("network %s isn't a bridge network with a subnet", d.Network)
	}
	allow, err := d.allowed(ctx)
	if err != nil {
		return err
	}
	if err := d.pull(ctx, d.FirewallImage); err != nil {
		return fmt.Errorf("pulling the firewall image: %w", err)
	}
	created, err := d.Docker.ContainerCreate(ctx, &container.Config{Image: d.FirewallImage, Env: []string{
		"OGIT_SUBNET=" + n.IPAM.Config[0].Subnet, "OGIT_BRIDGE=" + bridgeName(n),
		"OGIT_ALLOW=" + strings.Join(allow, " "), "OGIT_PEERS=" + strings.Join(d.FirewallPeers, " "),
	}}, &container.HostConfig{NetworkMode: "host", CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN", "NET_RAW"}}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("the firewall helper: %w", err)
	}
	defer func() {
		_ = d.Docker.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	if err := d.Docker.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return fmt.Errorf("the firewall helper: %w", err)
	}
	status, errs := d.Docker.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errs:
		return fmt.Errorf("the firewall helper: %w", err)
	case result := <-status:
		if result.StatusCode == 0 {
			return nil
		}
	}
	logs, err := d.Docker.ContainerLogs(ctx, created.ID, container.LogsOptions{ShowStderr: true, ShowStdout: true, Tail: "5"})
	message := "no output"
	if err == nil {
		defer logs.Close()
		var output bytes.Buffer
		if _, err := stdcopy.StdCopy(&output, &output, io.LimitReader(logs, 2048)); err == nil || output.Len() > 0 {
			message = strings.TrimSpace(output.String())
		}
	}
	return fmt.Errorf("the firewall rules failed: %s", message)
}
