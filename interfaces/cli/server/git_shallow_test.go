package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShallowFetches: git clone/fetch --depth with the real git client,
// over SSH and HTTP (actions/checkout fetches with --depth=1).
func TestShallowFetches(t *testing.T) {
	t.Run("local", func(t *testing.T) { testShallowFetches(t) })
	t.Run("s3_fake", func(t *testing.T) { testShallowFetches(t, withS3(s3test.StartFake(t))) })
}

func testShallowFetches(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Deep", false)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	// 20 commits on main, then a merge of a two-commit branch, and an
	// annotated tag on the merge.
	writer := t.TempDir()
	g.mustRun(writer, "clone", "-q", g.url("owner/deep"), ".")
	for i := 0; i < 20; i++ {
		commitFile(t, g, writer, fmt.Sprintf("f%d.txt", i%5), fmt.Sprintf("main %d\n", i))
	}
	g.mustRun(writer, "checkout", "-q", "-b", "side", "HEAD~3")
	commitFile(t, g, writer, "side.txt", "side 1\n")
	commitFile(t, g, writer, "side.txt", "side 2\n")
	g.mustRun(writer, "checkout", "-q", "main")
	g.mustRun(writer, "merge", "-q", "--no-ff", "-m", "Merge side", "side")
	g.mustRun(writer, "tag", "-a", "v1", "-m", "Version 1")
	g.mustRun(writer, "push", "-q", "origin", "main", "side", "v1")
	tip := strings.TrimSpace(g.mustRun(writer, "rev-parse", "HEAD"))

	// A commit only a deleted branch had: its objects stay in storage, but
	// nothing reaches it.
	g.mustRun(writer, "checkout", "-q", "-b", "gone")
	commitFile(t, g, writer, "secret.txt", "removed\n")
	gone := strings.TrimSpace(g.mustRun(writer, "rev-parse", "HEAD"))
	g.mustRun(writer, "push", "-q", "origin", "gone")
	g.mustRun(writer, "checkout", "-q", "main")
	g.mustRun(writer, "push", "-q", "origin", "--delete", "gone")

	count := func(g *gitCLI, dir, rev string) string {
		return strings.TrimSpace(g.mustRun(dir, "rev-list", "--count", rev))
	}
	// git's own answer for a depth: the same fetch from the writer, through
	// git's upload-pack (file://).
	oracle := func(g *gitCLI, depth, branch string) string {
		dir := t.TempDir()
		g.mustRun(dir, "clone", "-q", "--depth="+depth, "--branch", branch, "file://"+writer, ".")
		return count(g, dir, "HEAD")
	}
	shallowFile := func(dir string) string {
		b, _ := os.ReadFile(filepath.Join(dir, ".git", "shallow"))
		return string(b)
	}

	for _, transport := range []string{"ssh", "http"} {
		t.Run(transport, func(t *testing.T) {
			g := g.forTest(t)
			url := g.url("owner/deep")
			if transport == "http" {
				url = srv.url + "/owner/deep.git"
			}

			// --depth=1: the tip only, marked shallow, readable and consistent.
			one := t.TempDir()
			out := g.mustRun(one, "clone", "--depth=1", "--no-single-branch", url, ".")
			assert.NotContains(t, out, "does not support shallow")
			assert.Equal(t, "1", count(g, one, "HEAD"))
			assert.Contains(t, shallowFile(one), tip, "the tip is the shallow boundary")
			content, err := os.ReadFile(filepath.Join(one, "side.txt"))
			require.NoError(t, err)
			assert.Equal(t, "side 2\n", string(content), "the merge's whole tree is there")
			g.mustRun(one, "fsck", "--no-progress")

			// Deepening: more history, the old boundary moves down.
			g.mustRun(one, "fetch", "-q", "--depth=5", "origin", "main")
			assert.Equal(t, oracle(g, "5", "main"), count(g, one, "origin/main"), "the commits within depth 5, as git counts them")
			assert.NotContains(t, shallowFile(one), tip, "the tip isn't shallow any more (unshallow)")
			g.mustRun(one, "fsck", "--no-progress")

			// A normal fetch into a shallow clone gets just the new commit.
			commitFile(t, g, writer, "new-"+transport+".txt", transport)
			g.mustRun(writer, "push", "-q", "origin", "main")
			before, objects := count(g, one, "origin/main"), objectCount(t, g, one)
			g.mustRun(one, "fetch", "-q", "origin", "main")
			assert.Equal(t, fmt.Sprint(atoi(t, before)+1), count(g, one, "origin/main"), "one more commit")
			assert.LessOrEqual(t, objectCount(t, g, one)-objects, 3, "only the new commit, tree and blob: not the history below the boundary")
			g.mustRun(one, "fsck", "--no-progress")

			// --unshallow: the whole history, a complete repository.
			g.mustRun(one, "fetch", "-q", "--unshallow", "origin")
			assert.NoFileExists(t, filepath.Join(one, ".git", "shallow"))
			assert.Equal(t, strings.TrimSpace(g.mustRun(writer, "rev-list", "--count", "main")), count(g, one, "origin/main"))
			g.mustRun(one, "fsck", "--no-progress", "--strict")

			// --depth=2 at the merge: both parents, cut below them.
			two := t.TempDir()
			g.mustRun(two, "clone", "-q", "--depth=2", "--branch", "v1", url, ".")
			assert.Equal(t, oracle(g, "2", "v1"), count(g, two, "HEAD"), "the merge and its two parents")
			assert.Equal(t, "3", count(g, two, "HEAD"))
			assert.Len(t, strings.Fields(shallowFile(two)), 2, "both parents are boundaries")
			assert.Contains(t, g.mustRun(two, "cat-file", "-t", "v1"), "tag", "the annotated tag came along")
			g.mustRun(two, "fsck", "--no-progress")

			// What actions/checkout runs: a fetch of one commit by id, depth 1;
			// main has moved since, so it's no longer a tip.
			ci := t.TempDir()
			g.mustRun(ci, "init", "-q")
			g.mustRun(ci, "remote", "add", "origin", url)
			out, err = g.run(ci, "-c", "protocol.version=2", "fetch", "--no-tags", "--prune", "--no-recurse-submodules", "--depth=1", "origin",
				"+"+tip+":refs/remotes/origin/main")
			require.NoError(t, err, "%s", out)
			g.mustRun(ci, "checkout", "-q", "--progress", "--force", "-B", "main", "refs/remotes/origin/main")
			assert.Equal(t, "1", count(g, ci, "HEAD"))
			assert.FileExists(t, filepath.Join(ci, "side.txt"))

			// A commit no ref reaches is refused, by id or shallow.
			for _, depth := range []string{"--depth=1", "--no-tags"} {
				dir := t.TempDir()
				g.mustRun(dir, "init", "-q")
				out, err = g.run(dir, "fetch", depth, url, gone)
				assert.Error(t, err, "%s: %s", depth, out)
			}

			// Only --depth: the other shallow options are refused, clearly.
			since := t.TempDir()
			out, err = g.run(since, "clone", "--shallow-since=2000-01-01", url, ".")
			assert.Error(t, err)
			assert.Contains(t, out, "shallow-since", "%s", out)
		})
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	_, err := fmt.Sscan(s, &n)
	require.NoError(t, err)
	return n
}

// forTest is g for a subtest, so its failures are the subtest's.
func (g *gitCLI) forTest(t *testing.T) *gitCLI {
	c := *g
	c.t = t
	return &c
}
