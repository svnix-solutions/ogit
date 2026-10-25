package server

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withMaxPushSize(n int64) func(*config.Config) {
	return func(c *config.Config) { c.Git.MaxPushSize = n }
}

// randomFile writes n incompressible bytes (git can't shrink them).
func randomFile(t *testing.T, path string, n int) []byte {
	t.Helper()
	data := make([]byte, n)
	_, err := rand.Read(data)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return data
}

// Pushes larger than fiber's 4 MB body limit, with the real git client,
// over HTTP and SSH; and the git.max_push_size limit.
func TestLargePushes(t *testing.T) {
	t.Run("local", func(t *testing.T) { testLargePushes(t) })
	t.Run("s3_fake", func(t *testing.T) { testLargePushes(t, withS3(s3test.StartFake(t))) })
}

func testLargePushes(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, withMaxPushSize(20<<20))...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Big", true)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)
	httpURL := strings.Replace(srv.url, "http://", fmt.Sprintf("http://%s:%s@", owner.git.Username, owner.git.Password), 1) + "/owner/big.git"

	for _, transport := range []string{"http", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			url := httpURL
			if transport == "ssh" {
				url = g.url("owner/big")
			}
			dir := t.TempDir()
			g.mustRun(dir, "clone", "-q", url, ".")

			name := "blob-" + transport + ".bin"
			data := randomFile(t, filepath.Join(dir, name), 12<<20)
			g.mustRun(dir, "add", name)
			g.mustRun(dir, "commit", "-qm", "12 MB")
			out, err := g.run(dir, "push", "origin", "HEAD")
			require.NoError(t, err, "a 12 MB push: %s", out)

			check := t.TempDir()
			g.mustRun(check, "clone", "-q", url, ".")
			got, err := os.ReadFile(filepath.Join(check, name))
			require.NoError(t, err)
			assert.True(t, bytes.Equal(data, got), "the file comes back intact")

			tooBig := "too-big-" + transport + ".bin"
			randomFile(t, filepath.Join(dir, tooBig), 25<<20)
			g.mustRun(dir, "add", tooBig)
			g.mustRun(dir, "commit", "-qm", "25 MB")
			out, err = g.run(dir, "push", "origin", "HEAD")
			require.Error(t, err, "over git.max_push_size: %s", out)
			assert.Contains(t, out, "push is larger than the limit")

			// Nothing changed: the branch, and (on disk) no partial pack.
			g.mustRun(dir, "reset", "-q", "--hard", "HEAD~1")
			out, err = g.run(dir, "fetch", "origin")
			require.NoError(t, err, out)
			assert.Equal(t, strings.TrimSpace(g.mustRun(dir, "rev-parse", "HEAD")), strings.TrimSpace(g.mustRun(dir, "rev-parse", "origin/main")))
			if srv.deps.Config.Storage.Type == "local" {
				packs, _ := filepath.Glob(filepath.Join(srv.deps.Config.Storage.Local.Path, srv.repoStorageKey(t, "owner/big")+".git", "objects", "pack", "*"))
				for _, p := range packs {
					info, err := os.Stat(p)
					require.NoError(t, err)
					assert.Less(t, info.Size(), int64(20<<20), "no partial pack left: %s", p)
				}
			}
		})
	}
}

// A streamed route that answers without reading its body (here a refused
// upload) must not let the rest of the body be read as another request on
// the same connection (a desync behind a proxy reusing connections).
func TestStreamedBodiesDontDesync(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", true)
	for _, path := range []string{"/api/v1/repository/owner%2Fapp/releases/nope/assets?name=a", "/owner/app.git/git-receive-pack"} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(srv.url, "http://"))
		require.NoError(t, err)
		smuggled := "GET /api/v1/instance HTTP/1.1\r\nHost: x\r\n\r\n"
		_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n%s", path, smuggled)
		require.NoError(t, err)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
		all, _ := io.ReadAll(conn)
		conn.Close()
		assert.Equal(t, 1, strings.Count(string(all), "HTTP/1.1 "), "%s: one answer, then the connection is closed:\n%s", path, all)
		assert.NotContains(t, string(all), "registration_enabled", path)
	}
}
