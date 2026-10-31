//go:build integration

package server

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDockerRegistry pushes and pulls with the real docker client. Docker
// talks plain HTTP to 127.0.0.1, so the daemon must run on this host:
// Docker Desktop's runs in a VM, where 127.0.0.1 is not this machine.
func TestDockerRegistry(t *testing.T) {
	info, err := exec.Command("docker", "info", "--format", "{{.OperatingSystem}}").Output()
	if err != nil {
		t.Skip("docker is not available")
	}
	if strings.Contains(string(info), "Docker Desktop") {
		t.Skip("Docker Desktop's daemon can't reach 127.0.0.1 on this host")
	}
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	token := srv.createToken(t, alice.session, "docker", "write:packages")
	u, err := url.Parse(srv.url)
	require.NoError(t, err)
	image := u.Host + "/alice/hello:v1"

	dockerConfig := t.TempDir()
	docker := func(stdin string, args ...string) (string, error) {
		cmd := exec.Command("docker", args...)
		cmd.Env = append(os.Environ(), "DOCKER_CONFIG="+dockerConfig)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { _, _ = docker("", "rmi", "-f", image) })

	// FROM scratch: nothing to pull.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\nCOPY hello.txt /\n"), 0o644))
	out, err := docker("", "build", "-t", image, dir)
	require.NoError(t, err, out)

	out, err = docker("", "push", image)
	require.Error(t, err, "not logged in: %s", out)
	out, err = docker(token.Password, "login", u.Host, "-u", "alice", "--password-stdin")
	require.NoError(t, err, out)
	out, err = docker("", "push", image)
	require.NoError(t, err, out)

	out, err = docker("", "rmi", image)
	require.NoError(t, err, out)
	out, err = docker("", "pull", image)
	require.NoError(t, err, out)
	out, err = docker("", "logout", u.Host)
	require.NoError(t, err, out)
	_, _ = docker("", "rmi", image)
	out, err = docker("", "pull", image)
	assert.Error(t, err, "private: %s", out)

	status, body := srv.get(t, "/api/v1/packages/alice/container/hello", alice.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	// Newer Docker pushes an index and its manifests, which stay untagged
	// versions; older Docker pushes one manifest. Either way one tag.
	var tags []any
	for _, v := range body["versions"].([]any) {
		versionTags, _ := v.(map[string]any)["tags"].([]any)
		tags = append(tags, versionTags...)
	}
	assert.Equal(t, []any{"v1"}, tags)
}
