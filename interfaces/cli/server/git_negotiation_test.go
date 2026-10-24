package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// objectCount returns how many objects (loose + packed) a clone holds.
func objectCount(t *testing.T, g *gitCLI, dir string) int {
	t.Helper()
	total := 0
	for _, line := range strings.Split(g.mustRun(dir, "count-objects", "-v"), "\n") {
		for _, key := range []string{"count: ", "in-pack: "} {
			if strings.HasPrefix(line, key) {
				n, err := strconv.Atoi(strings.TrimPrefix(line, key))
				require.NoError(t, err)
				total += n
			}
		}
	}
	return total
}

// TestFetchNegotiation checks, with the real git client over SSH and HTTP,
// that fetching one new commit into a clone with a long history transfers
// only the new objects. Without negotiation the fetch failed once the client
// needed a second round of haves (SSH: "bad line length character: PACK"),
// or re-sent most of the history (HTTP).
func TestFetchNegotiation(t *testing.T) {
	t.Run("local", func(t *testing.T) { testFetchNegotiation(t) })
	t.Run("s3_fake", func(t *testing.T) { testFetchNegotiation(t, withS3(s3test.StartFake(t))) })
}

func testFetchNegotiation(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "History", false)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	// 300 commits: well past the 32 haves of a first negotiation round.
	writer := t.TempDir()
	g.mustRun(writer, "clone", g.url("history"), ".")
	for i := 0; i < 300; i++ {
		require.NoError(t, os.WriteFile(filepath.Join(writer, fmt.Sprintf("f%d.txt", i%20)), []byte(fmt.Sprintf("content %d\n", i)), 0o644))
		g.mustRun(writer, "add", "-A")
		g.mustRun(writer, "commit", "-qm", fmt.Sprintf("c%d", i))
	}
	g.mustRun(writer, "push", "-q", "origin", "HEAD")

	for _, transport := range []string{"ssh", "http"} {
		t.Run(transport, func(t *testing.T) {
			url := g.url("history")
			if transport == "http" {
				url = srv.url + "/history.git"
			}
			reader := t.TempDir()
			g.mustRun(reader, "clone", "-q", url, ".")

			commitFile(t, g, writer, "new-"+transport+".txt", transport)
			g.mustRun(writer, "push", "-q", "origin", "HEAD")

			before := objectCount(t, g, reader)
			out := g.mustRun(reader, "fetch", "origin")
			assert.NotContains(t, out, "no common commits")
			assert.LessOrEqual(t, objectCount(t, g, reader)-before, 3, "only the new commit, tree and blob")

			// Unpushed local commits: haves the server has never seen.
			for i := 0; i < 40; i++ {
				commitFile(t, g, reader, "local.txt", fmt.Sprint(i))
			}
			commitFile(t, g, writer, "more-"+transport+".txt", transport)
			g.mustRun(writer, "push", "-q", "origin", "HEAD")
			before = objectCount(t, g, reader)
			g.mustRun(reader, "fetch", "origin")
			assert.LessOrEqual(t, objectCount(t, g, reader)-before, 3)

			assert.Empty(t, regexp.MustCompile(`(?m)^\s*\S`).FindString(g.mustRun(reader, "fetch", "origin")), "up to date: nothing fetched")

			// Unrelated history: no have is common, so every round must be
			// answered NAK until the client gives up and sends done.
			unrelated := t.TempDir()
			g.mustRun(unrelated, "init", "-q")
			for i := 0; i < 70; i++ {
				commitFile(t, g, unrelated, "own.txt", fmt.Sprint(i))
			}
			g.mustRun(unrelated, "fetch", "-q", url, "main")
			assert.Contains(t, g.mustRun(unrelated, "log", "--oneline", "-1", "FETCH_HEAD"), "update more-"+transport)
		})
	}
}

// TestPushDeltaAgainstExistingObjects pushes an edit git sends as a delta
// against a blob the server already has. git pushes thin packs unless the
// server advertises no-thin, and go-git cannot resolve thin packs ("remote
// unpack failed: reference delta not found").
func TestPushDeltaAgainstExistingObjects(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPushDelta(t) })
	t.Run("s3_fake", func(t *testing.T) { testPushDelta(t, withS3(s3test.StartFake(t))) })
}

func testPushDelta(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Deltas", true)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	big := strings.Repeat("a line of text that git will delta-compress\n", 2000)
	for _, transport := range []string{"ssh", "http"} {
		t.Run(transport, func(t *testing.T) {
			url := g.url("deltas")
			if transport == "http" {
				url = strings.Replace(srv.url, "http://", fmt.Sprintf("http://%s:%s@", owner.git.Username, owner.git.Password), 1) + "/deltas.git"
			}
			dir := t.TempDir()
			g.mustRun(dir, "clone", "-q", url, ".")
			name := "big-" + transport + ".txt"
			commitFile(t, g, dir, name, big)
			g.mustRun(dir, "push", "-q", "origin", "HEAD")

			commitFile(t, g, dir, name, big+"one more line\n")
			out, err := g.run(dir, "push", "origin", "HEAD")
			require.NoError(t, err, out)
			assert.NotContains(t, out, "reference delta not found")

			check := t.TempDir()
			g.mustRun(check, "clone", "-q", url, ".")
			content, err := os.ReadFile(filepath.Join(check, name))
			require.NoError(t, err)
			assert.True(t, strings.HasSuffix(string(content), "one more line\n"), "the pushed edit is there")
		})
	}
}
