//go:build integration

package wsagent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testToken = "ogitw_integrationtesttoken"

// gitServer serves a bare repository through git http-backend, for testToken
// only, on an address containers reach.
func gitServer(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	work := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	run(work, "init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello\n"), 0o644))
	run(work, "add", ".")
	run(work, "commit", "-m", "base")
	run(root, "clone", "--bare", work, "app.git")
	run(filepath.Join(root, "app.git"), "config", "http.receivepack", "true")
	backend, err := exec.LookPath("git")
	require.NoError(t, err)
	handler := &cgi.Handler{Path: backend, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, password, _ := r.BasicAuth(); password != testToken {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return fmt.Sprintf("%d", listener.Addr().(*net.TCPAddr).Port)
}

// hostFor is the host's address from a container.
func hostFor(t *testing.T, d *DockerRuntime) string {
	t.Helper()
	info, err := d.Docker.Info(context.Background())
	require.NoError(t, err)
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		return "host.docker.internal"
	}
	out, err := exec.Command("docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

func TestDockerWorkspace(t *testing.T) {
	d, err := NewDockerRuntime("", "", "")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := d.Docker.Ping(ctx); err != nil {
		t.Skip("docker is not available")
	}
	d.GitURL = "http://" + hostFor(t, d) + ":" + gitServer(t)
	d.NoFirewall = true

	id := "00000000-0000-4000-8000-" + fmt.Sprintf("%012d", time.Now().UnixNano()%1_000_000_000_000)
	task := application.WorkspaceTask{Id: id, Image: config.DefaultWorkspaceImage, Repository: "app.git", Branch: "main", Token: testToken,
		CPUs: 1, MemoryGB: 1, DiskGB: 1, Pids: 256}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })
	require.NoError(t, d.Start(ctx, task))

	running, err := d.Running(ctx)
	require.NoError(t, err)
	assert.Contains(t, running, id)

	inspect, err := d.Docker.ContainerInspect(ctx, containerName(id))
	require.NoError(t, err)
	host := inspect.HostConfig
	assert.Equal(t, "1000:1000", inspect.Config.User)
	assert.Contains(t, []string(host.CapDrop), "ALL")
	assert.Contains(t, host.SecurityOpt, "no-new-privileges")
	assert.True(t, host.ReadonlyRootfs)
	assert.False(t, host.Privileged)
	assert.Empty(t, host.PortBindings, "nothing is published")
	assert.False(t, host.PublishAllPorts)
	assert.Equal(t, int64(1_000_000_000), host.NanoCPUs)
	assert.Equal(t, gb, host.Memory)
	assert.Equal(t, gb, host.MemorySwap)
	assert.Equal(t, int64(256), *host.PidsLimit)
	for _, e := range inspect.Config.Env {
		assert.NotContains(t, e, testToken)
	}
	assert.NotContains(t, strings.Join(inspect.Config.Cmd, " "), testToken)

	sh := func(script string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", "exec", "-u", workspaceUser, "-e", "HOME="+homeDir, containerName(id), "sh", "-c", script).CombinedOutput()
		return string(out), err
	}
	var health string
	for range 60 {
		if health, err = sh("curl -sf http://127.0.0.1:8080/healthz"); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	require.NoError(t, err, "the editor listens on a read-only root: %s", health)

	out, err := sh("cat " + cloneDir + "/hello.txt")
	require.NoError(t, err, out)
	assert.Equal(t, "hello\n", out, "cloned")

	out, err = sh("cd " + cloneDir + " && git config user.email a@b && git config user.name a && git commit --allow-empty -qm work && git push -q origin HEAD:refs/heads/from-workspace")
	assert.NoError(t, err, "the credential helper pushes: %s", out)

	out, err = sh("grep -rl " + testToken + " " + homeDir + " 2>/dev/null; true")
	require.NoError(t, err)
	assert.Empty(t, strings.TrimSpace(out), "the token isn't in the volume")

	_, err = sh("touch /usr/oops")
	assert.Error(t, err, "the root is read-only")
	_, err = sh("sudo -n true")
	assert.Error(t, err, "no way up")

	logs, err := d.Docker.ContainerLogs(ctx, containerName(id), container.LogsOptions{ShowStdout: true, ShowStderr: true})
	require.NoError(t, err)
	defer logs.Close()
	data := make([]byte, 1<<20)
	n, _ := logs.Read(data)
	assert.NotContains(t, string(data[:n]), testToken)

	require.NoError(t, d.Stop(ctx, id))
	running, err = d.Running(ctx)
	require.NoError(t, err)
	assert.NotContains(t, running, id)

	require.NoError(t, d.Start(ctx, task), "a restart keeps the clone")
	out, err = sh("cd " + cloneDir + " && git log -1 --format=%s")
	require.NoError(t, err, out)
	assert.Equal(t, "work\n", out)

	address, err := d.Address(ctx, id)
	require.NoError(t, err)
	assert.NotNil(t, net.ParseIP(address), address)
	_, err = d.Address(ctx, "00000000-0000-4000-8000-000000000000")
	assert.Error(t, err)

	over, err := d.OverDisk(ctx)
	require.NoError(t, err)
	assert.NotContains(t, over, id)

	require.NoError(t, d.Remove(ctx, id))
	require.NoError(t, d.Remove(ctx, id), "again is fine")
	_, err = d.Docker.VolumeInspect(ctx, volumeName(id))
	assert.Error(t, err, "the volume is gone")

	bad := task
	bad.Image = "codercom/code-server:latest"
	assert.ErrorContains(t, d.Start(ctx, bad), "digest")
}

// firewallTask is a small workspace.
func firewallTask(id string) application.WorkspaceTask {
	return application.WorkspaceTask{Id: id, Image: config.DefaultWorkspaceImage, Repository: "app.git", Branch: "main", Token: testToken,
		CPUs: 1, MemoryGB: 1, DiskGB: 1, Pids: 256}
}

func TestDockerWorkspaceFirewall(t *testing.T) {
	d, err := NewDockerRuntime("", "", "ogit-firewall-test")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := d.Docker.Ping(ctx); err != nil {
		t.Skip("docker is not available")
	}
	info, err := d.Docker.Info(ctx)
	require.NoError(t, err)
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		t.Skip("the rules are for Linux hosts")
	}
	const image = "ogit-workspace-firewall:test"
	out, err := exec.CommandContext(ctx, "docker", "build", "-q", "-t", image, "../../docker/workspace-firewall").CombinedOutput()
	require.NoError(t, err, string(out))
	host := hostFor(t, d)
	d.GitURL = "http://" + host + ":" + gitServer(t)
	refused := gitServer(t) // on the host too, but not the allowed port

	d.FirewallImage = image
	base := fmt.Sprintf("%012d", time.Now().UnixNano()%1_000_000_000_000)
	one, two := "00000000-0000-4000-8001-"+base, "00000000-0000-4000-8002-"+base
	t.Cleanup(func() {
		_ = d.Remove(context.Background(), one)
		_ = d.Remove(context.Background(), two)
		_ = d.Docker.NetworkRemove(context.Background(), d.Network)
	})
	require.NoError(t, d.Start(ctx, firewallTask(one)), "the clone needs the server: it stays reachable")
	require.NoError(t, d.Start(ctx, firewallTask(two)), "running the rules again is fine")

	reach := func(url string) string {
		out, _ := exec.CommandContext(ctx, "docker", "exec", containerName(one), "curl", "-s", "-o", "/dev/null", "-m", "4", "-w", "%{http_code}", url).CombinedOutput()
		return string(out)
	}
	assert.NotEqual(t, "000", reach("https://github.com"), "the internet is open")
	assert.Equal(t, "000", reach("http://169.254.169.254/"), "cloud metadata")
	assert.Equal(t, "000", reach("http://"+host+":"+refused+"/"), "the host's other services")
	assert.Equal(t, "000", reach("http://10.255.255.1/"), "private ranges")
	neighbour, err := d.Address(ctx, two)
	require.NoError(t, err)
	var health string
	for range 60 {
		if health = reachFrom(ctx, containerName(two), "http://127.0.0.1:8080/healthz"); health == "200" {
			break
		}
		time.Sleep(time.Second)
	}
	require.Equal(t, "200", health)
	assert.Equal(t, "000", reach("http://"+neighbour+":8080/healthz"), "other workspaces")

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + neighbour + ":8080/healthz")
	require.NoError(t, err, "the agent's host still reaches workspaces")
	resp.Body.Close()

	d.FirewallImage = ""
	assert.ErrorIs(t, d.Start(ctx, firewallTask(one)), ErrNoFirewallImage, "no rules, no workspace")
	d.FirewallImage = "ogit-workspace-firewall:missing@sha256:" + strings.Repeat("0", 64)
	assert.Error(t, d.Start(ctx, firewallTask(one)))
}

func reachFrom(ctx context.Context, name, url string) string {
	out, _ := exec.CommandContext(ctx, "docker", "exec", name, "curl", "-s", "-o", "/dev/null", "-m", "4", "-w", "%{http_code}", url).CombinedOutput()
	return string(out)
}
