package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	sshserver "github.com/labbs/ogit/interfaces/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// startSSH serves git over SSH for srv on a random local port.
func (s *testServer) startSSH(t *testing.T) int {
	t.Helper()
	srv, err := sshserver.New(filepath.Join(t.TempDir(), "host_key"), s.deps.Logger, s.deps.AuthApp, s.deps.SSHKeyApp.SSHKeyPers, s.deps.Storage)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// sshKey is a client key pair on disk.
type sshKey struct {
	privatePath string
	authorized  string // authorized_keys line
	signer      gossh.Signer
}

func newSSHKey(t *testing.T, comment string) sshKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := gossh.MarshalPrivateKey(priv, comment)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "id_ed25519")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(block), 0o600))
	sshPub, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	signer, err := gossh.NewSignerFromKey(priv)
	require.NoError(t, err)
	return sshKey{privatePath: path, authorized: strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub))) + " " + comment, signer: signer}
}

// gitCLI runs the real git binary with key over SSH, failing on hangs.
type gitCLI struct {
	t    *testing.T
	key  sshKey
	port int
	home string
}

func newGitCLI(t *testing.T, key sshKey, port int) *gitCLI {
	t.Helper()
	for _, bin := range []string{"git", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			// In CI these tests must run, not silently skip.
			if os.Getenv("CI") != "" {
				t.Fatalf("%s not installed; the SSH tests need the real client", bin)
			}
			t.Skipf("%s not installed", bin)
		}
	}
	return &gitCLI{t: t, key: key, port: port, home: t.TempDir()}
}

func (g *gitCLI) url(slug string) string {
	return fmt.Sprintf("ssh://git@127.0.0.1:%d/%s.git", g.port, slug)
}

// run executes git in dir and returns combined output and the error.
func (g *gitCLI) run(dir string, args ...string) (string, error) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"HOME="+g.home, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_SSH_COMMAND=ssh -i "+g.key.privatePath+" -F /dev/null -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		g.t.Fatalf("git %s timed out (hang):\n%s", strings.Join(args, " "), out.String())
	}
	return out.String(), err
}

func (g *gitCLI) mustRun(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.run(dir, args...)
	require.NoError(g.t, err, "git %s:\n%s", strings.Join(args, " "), out)
	return out
}

// sshRun runs the real ssh client and returns its combined output.
func (g *gitCLI) ssh(user string, args ...string) (string, error) {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := []string{"-i", g.key.privatePath, "-F", "/dev/null", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "-o", "BatchMode=yes", "-p", fmt.Sprint(g.port), user + "@127.0.0.1"}
	cmd := exec.CommandContext(ctx, "ssh", append(base, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+g.home)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		g.t.Fatalf("ssh timed out (hang):\n%s", out.String())
	}
	return out.String(), err
}

func commitFile(t *testing.T, g *gitCLI, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	g.mustRun(dir, "add", name)
	g.mustRun(dir, "commit", "-qm", "update "+name)
}

// TestGitOverSSH runs the real git client through clone, push, fetch and
// branch operations on every storage backend.
func TestGitOverSSH(t *testing.T) {
	t.Run("local", func(t *testing.T) { testGitOverSSH(t) })
	t.Run("s3_fake", func(t *testing.T) { testGitOverSSH(t, withS3(s3test.StartFake(t))) })
}

func testGitOverSSH(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Over SSH", true)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, body := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)

	g := newGitCLI(t, key, port)
	a, b := t.TempDir(), t.TempDir()

	out := g.mustRun(a, "clone", g.url("over-ssh"), ".")
	assert.NotContains(t, out, "hung up")
	assert.FileExists(t, filepath.Join(a, "README.md"))

	commitFile(t, g, a, "f.txt", "1\n")
	out = g.mustRun(a, "push", "origin", "HEAD")
	assert.NotContains(t, out, "hung up")
	assert.Contains(t, g.mustRun(a, "push", "origin", "HEAD"), "Everything up-to-date")

	g.mustRun(b, "clone", g.url("over-ssh"), ".")
	commitFile(t, g, a, "f.txt", "2\n")
	g.mustRun(a, "push", "origin", "HEAD")
	g.mustRun(b, "pull", "--ff-only", "origin", "main")
	content, err := os.ReadFile(filepath.Join(b, "f.txt"))
	require.NoError(t, err)
	assert.Equal(t, "2\n", string(content), "fetch sees the new push")
	g.mustRun(b, "fetch", "origin") // up to date

	g.mustRun(a, "push", "origin", "HEAD:refs/heads/feature")
	assert.Contains(t, g.mustRun(a, "ls-remote", "origin"), "refs/heads/feature")
	g.mustRun(a, "push", "origin", "--delete", "feature")
	refs := g.mustRun(a, "ls-remote", "origin")
	assert.NotContains(t, refs, "refs/heads/feature", "delete-only push works")
	assert.NotContains(t, refs, "refs/remotes/", "no stray refs")
}

func TestSSHAccessRules(t *testing.T) {
	srv := startTestServer(t)
	owner, reporter, outsider := srv.newUser(t, "owner"), srv.newUser(t, "reporter"), srv.newUser(t, "outsider")
	srv.createRepo(t, owner.session, "Private Repo", true)
	srv.createRepo(t, owner.session, "Other Repo", true)
	status, _ := srv.request(t, http.MethodPost, "/api/v1/repository/private-repo/members", owner.session, map[string]any{"username": "reporter", "role": "reporter"})
	require.Equal(t, http.StatusOK, status)
	port := srv.startSSH(t)

	addKey := func(u user, comment string) *gitCLI {
		key := newSSHKey(t, comment)
		status, body := srv.post(t, "/api/v1/user/ssh-keys", u.session, map[string]any{"key": key.authorized})
		require.Equal(t, http.StatusOK, status, "%v", body)
		return newGitCLI(t, key, port)
	}
	ownerGit, reporterGit, outsiderGit := addKey(owner, "owner@x"), addKey(reporter, "reporter@x"), addKey(outsider, "outsider@x")
	push := func(g *gitCLI, slug string) (string, error) {
		dir := t.TempDir()
		ownerGit.mustRun(dir, "clone", ownerGit.url(slug), ".")
		commitFile(t, ownerGit, dir, "x.txt", time.Now().String())
		return g.run(dir, "push", g.url(slug), "HEAD:main")
	}

	t.Run("greeting_instead_of_shell", func(t *testing.T) {
		out, err := ownerGit.ssh("git")
		assert.Error(t, err, "exit status 1")
		assert.Contains(t, out, "Hi owner! You've successfully authenticated, but oGit does not provide shell access.")
	})

	t.Run("only_git_user", func(t *testing.T) {
		out, err := ownerGit.ssh("owner")
		assert.Error(t, err)
		assert.Contains(t, out, "Permission denied (publickey)", "rejected at authentication, not just by the shell")
	})

	t.Run("unknown_key", func(t *testing.T) {
		g := newGitCLI(t, newSSHKey(t, "stranger"), port)
		out, err := g.run(t.TempDir(), "ls-remote", g.url("private-repo"))
		assert.Error(t, err)
		assert.Contains(t, out, "Permission denied (publickey)")
	})

	t.Run("roles", func(t *testing.T) {
		_, err := reporterGit.run(t.TempDir(), "ls-remote", reporterGit.url("private-repo"))
		assert.NoError(t, err, "reporters read")
		out, err := push(reporterGit, "private-repo")
		assert.Error(t, err)
		assert.Contains(t, out, "permission to private-repo denied to reporter", "reporters cannot push")

		out, err = outsiderGit.run(t.TempDir(), "ls-remote", outsiderGit.url("private-repo"))
		assert.Error(t, err)
		assert.Contains(t, out, `repository "private-repo" not found`, "private repos are hidden")
		out, _ = outsiderGit.run(t.TempDir(), "ls-remote", outsiderGit.url("missing-repo"))
		assert.Contains(t, out, `repository "missing-repo" not found`, "same answer as a missing repo")

		_, err = push(ownerGit, "private-repo")
		assert.NoError(t, err)
	})

	t.Run("bad_commands", func(t *testing.T) {
		for _, cmd := range []string{"git-upload-pack '../etc'", "git-upload-pack 'a/b.git'", "ls -la", "git-upload-archive 'private-repo.git'"} {
			out, err := ownerGit.ssh("git", cmd)
			assert.Error(t, err, cmd)
			assert.Contains(t, out, "oGit only serves git-upload-pack and git-receive-pack", cmd)
		}
	})

	t.Run("deploy_keys", func(t *testing.T) {
		ro, rw := newSSHKey(t, "ci-read"), newSSHKey(t, "ci-write")
		status, body := srv.post(t, "/api/v1/repository/private-repo/deploy-keys", owner.session, map[string]any{"key": ro.authorized})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["read_only"], "read-only by default")
		status, _ = srv.post(t, "/api/v1/repository/private-repo/deploy-keys", owner.session, map[string]any{"key": rw.authorized, "read_only": false})
		require.Equal(t, http.StatusOK, status)

		roGit, rwGit := newGitCLI(t, ro, port), newGitCLI(t, rw, port)
		_, err := roGit.run(t.TempDir(), "ls-remote", roGit.url("private-repo"))
		assert.NoError(t, err)
		out, err := push(roGit, "private-repo")
		assert.Error(t, err)
		assert.Contains(t, out, "denied to deploy key ci-read")
		_, err = push(rwGit, "private-repo")
		assert.NoError(t, err, "read/write deploy keys push")

		out, err = roGit.run(t.TempDir(), "ls-remote", roGit.url("other-repo"))
		assert.Error(t, err)
		assert.Contains(t, out, `repository "other-repo" not found`, "deploy keys are limited to their repository")

		greeting, _ := roGit.ssh("git")
		assert.Contains(t, greeting, "Hi deploy key ci-read!")
	})

	t.Run("last_used_is_recorded", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/user/ssh-keys", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.NotNil(t, body["keys"].([]any)[0].(map[string]any)["last_used_at"])
	})

	t.Run("disabled_user_rejected", func(t *testing.T) {
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "reporter").Update("active", false).Error)
		out, err := reporterGit.run(t.TempDir(), "ls-remote", reporterGit.url("private-repo"))
		assert.Error(t, err)
		assert.Contains(t, out, "Permission denied (publickey)")
	})

	t.Run("deleted_key_rejected", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/user/ssh-keys", owner.session)
		require.Equal(t, http.StatusOK, status)
		id := body["keys"].([]any)[0].(map[string]any)["id"].(string)
		status, _ = srv.request(t, http.MethodDelete, "/api/v1/user/ssh-keys/"+id, owner.session, nil)
		require.Equal(t, http.StatusOK, status)
		out, err := ownerGit.run(t.TempDir(), "ls-remote", ownerGit.url("private-repo"))
		assert.Error(t, err)
		assert.Contains(t, out, "Permission denied (publickey)")
	})
}

func TestSSHHostKeyPersists(t *testing.T) {
	srv := startTestServer(t)
	path := filepath.Join(t.TempDir(), "keys", "ssh_host_ed25519_key")
	fingerprint := func() string {
		s, err := sshserver.New(path, srv.deps.Logger, srv.deps.AuthApp, srv.deps.SSHKeyApp.SSHKeyPers, srv.deps.Storage)
		require.NoError(t, err)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()
		go func() { _ = s.Serve(ln) }()

		var seen string
		cfg := &gossh.ClientConfig{
			User: "git", Auth: []gossh.AuthMethod{gossh.PublicKeys(newSSHKey(t, "x").signer)},
			HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
				seen = gossh.FingerprintSHA256(key)
				return nil
			},
		}
		if c, err := gossh.Dial("tcp", ln.Addr().String(), cfg); err == nil {
			c.Close()
		}
		require.NotEmpty(t, seen)
		return seen
	}

	first := fingerprint()
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "host key is private")
	assert.Equal(t, first, fingerprint(), "the same host key is used after a restart")
}
