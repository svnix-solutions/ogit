package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requestBudget is the most S3 requests one git operation may cost on a
// 300-commit repository. Storing one S3 object per git object cost about
// 1,800 for the push, 1,500 for a clone and 1,250 to fetch one commit.
const requestBudget = 25

// TestS3RequestBudget keeps S3 request counts from creeping back up: a
// regression here means seconds per git operation against real S3.
func TestS3RequestBudget(t *testing.T) {
	// Heavy and independent: runs alongside the other slow tests, after the
	// sequential ones.
	t.Parallel()
	for _, cacheType := range []string{"memory", "none"} {
		t.Run(cacheType, func(t *testing.T) {
			ep, counter := s3test.StartCountingFake(t)
			srv := startTestServer(t, func(c *config.Config) {
				s3test.Configure(c, ep)
				c.Storage.S3.Cache.Type = cacheType
			})
			owner := srv.newUser(t, "owner")
			srv.createRepo(t, owner.session, "Big", false)
			port := srv.startSSH(t)
			key := newSSHKey(t, "o")
			status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
			require.Equal(t, http.StatusOK, status)
			g := newGitCLI(t, key, port)

			writer := t.TempDir()
			g.mustRun(writer, "clone", "-q", g.url("owner/big"), ".")
			for i := 0; i < 300; i++ {
				require.NoError(t, os.WriteFile(filepath.Join(writer, fmt.Sprintf("f%d.txt", i%20)), []byte(fmt.Sprintf("content %d\n", i)), 0o644))
				g.mustRun(writer, "add", "-A")
				g.mustRun(writer, "commit", "-qm", fmt.Sprintf("c%d", i))
			}

			measure := func(name string, run func()) {
				t.Helper()
				counter.Reset()
				run()
				t.Logf("%-16s %3d requests %v", name, counter.Total(), counter)
				assert.LessOrEqual(t, counter.Total(), requestBudget, name)
			}
			reader := t.TempDir()
			measure("push 300 commits", func() { g.mustRun(writer, "push", "-q", "origin", "HEAD") })
			measure("clone", func() { g.mustRun(reader, "clone", "-q", g.url("owner/big"), ".") })
			assert.Positive(t, counter.Get("RANGE"), "objects are read from packs with ranged GETs")
			commitFile(t, g, writer, "x.txt", "x")
			measure("push 1 commit", func() { g.mustRun(writer, "push", "-q", "origin", "HEAD") })
			measure("fetch 1 commit", func() { g.mustRun(reader, "fetch", "-q", "origin") })
			measure("clone over HTTP", func() { g.mustRun(t.TempDir(), "clone", "-q", srv.url+"/owner/big.git", ".") })
		})
	}
}

// A repository stored before packs existed can be cloned and pushed to, and
// `ogit storage repack` converts it.
func TestLegacyS3RepositoryOverGit(t *testing.T) {
	ep, counter := s3test.StartCountingFake(t)
	srv := startTestServer(t, withS3(ep))
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Old", true)
	var repo domain.Repository
	require.NoError(t, srv.deps.Database.Db.Where("slug = ?", "old").First(&repo).Error)

	// Replace its data with the old loose layout under the same storage key.
	require.NoError(t, srv.deps.Storage.DeleteRepository(repo.StorageKey))
	s3test.CreateLegacyRepository(t, ep, repo.StorageKey, map[string]string{"README.md": "from the old layout\n"})

	port := srv.startSSH(t)
	key := newSSHKey(t, "o")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	dir := t.TempDir()
	g.mustRun(dir, "clone", "-q", g.url("owner/old"), ".")
	content, err := os.ReadFile(filepath.Join(dir, "README.md"))
	require.NoError(t, err)
	assert.Equal(t, "from the old layout\n", string(content))
	commitFile(t, g, dir, "new.txt", "pushed onto the old layout")
	g.mustRun(dir, "push", "-q", "origin", "HEAD")

	s3store, ok := srv.deps.Storage.(*s3.S3Storage)
	require.True(t, ok)
	n, err := s3store.Repack(repo.StorageKey)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "the legacy commit's blob, tree and commit (the push added a pack)")

	counter.Reset()
	check := t.TempDir()
	g.mustRun(check, "clone", "-q", g.url("owner/old"), ".")
	assert.FileExists(t, filepath.Join(check, "new.txt"), "both the old and the pushed history survive")
	assert.LessOrEqual(t, counter.Total(), requestBudget, "and the clone costs a handful of requests: %v", counter)
}

// Every push adds a pack, and every request lists and loads them all, so
// many pushes slow a repository down until `ogit storage gc` merges them.
func TestS3GCOverGit(t *testing.T) {
	ep, counter := s3test.StartCountingFake(t)
	srv := startTestServer(t, func(c *config.Config) {
		s3test.Configure(c, ep)
		c.Storage.S3.Cache.Type = "none" // count every index load
	})
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Busy", false)
	var repo domain.Repository
	require.NoError(t, srv.deps.Database.Db.Where("slug = ?", "busy").First(&repo).Error)
	port := srv.startSSH(t)
	key := newSSHKey(t, "o")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	writer := t.TempDir()
	g.mustRun(writer, "clone", "-q", g.url("owner/busy"), ".")
	for i := 0; i < 40; i++ {
		commitFile(t, g, writer, fmt.Sprintf("f%d.txt", i%5), fmt.Sprintf("push %d\n", i))
		g.mustRun(writer, "push", "-q", "origin", "HEAD")
	}

	clone := func() int {
		counter.Reset()
		g.mustRun(t.TempDir(), "clone", "-q", g.url("owner/busy"), ".")
		return counter.Total()
	}
	before := clone()

	s3store := srv.deps.Storage.(*s3.S3Storage)
	res, err := s3store.GC(repo.StorageKey, 0)
	require.NoError(t, err)
	assert.Equal(t, 41, res.Merged, "the initial pack and one per push")
	after := clone()
	t.Logf("clone: %d requests with 41 packs, %d after gc", before, after)
	assert.Greater(t, before, 2*after)
	assert.LessOrEqual(t, after, requestBudget)

	// The repository keeps working: fetch, push, and a second gc deletes
	// the replaced packs.
	reader := t.TempDir()
	g.mustRun(reader, "clone", "-q", g.url("owner/busy"), ".")
	commitFile(t, g, writer, "late.txt", "after gc")
	g.mustRun(writer, "push", "-q", "origin", "HEAD")
	g.mustRun(reader, "pull", "-q", "origin", "main")
	assert.FileExists(t, filepath.Join(reader, "late.txt"))
	res, err = s3store.GC(repo.StorageKey, 0)
	require.NoError(t, err)
	assert.Equal(t, 41, res.Deleted)
	g.mustRun(t.TempDir(), "clone", "-q", g.url("owner/busy"), ".")
	assert.Equal(t, "42", strings.TrimSpace(g.mustRun(reader, "rev-list", "--count", "HEAD")), "initial commit, 40 pushes and one after gc")
}

// After a force-push and a branch deletion, prune removes the dropped
// commits and the repository keeps working.
func TestS3PruneOverGit(t *testing.T) {
	srv := startTestServer(t, withS3(s3test.StartFake(t)))
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Forced", false)
	var repo domain.Repository
	require.NoError(t, srv.deps.Database.Db.Where("slug = ?", "forced").First(&repo).Error)
	port := srv.startSSH(t)
	key := newSSHKey(t, "o")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)

	dir := t.TempDir()
	g.mustRun(dir, "clone", "-q", g.url("owner/forced"), ".")
	for i := 0; i < 3; i++ {
		commitFile(t, g, dir, "f.txt", fmt.Sprintf("v%d\n", i))
		g.mustRun(dir, "push", "-q", "origin", "HEAD")
	}
	dropped := strings.TrimSpace(g.mustRun(dir, "rev-parse", "HEAD"))
	g.mustRun(dir, "checkout", "-qb", "feature")
	commitFile(t, g, dir, "feature.txt", "x")
	g.mustRun(dir, "push", "-q", "origin", "feature")
	feature := strings.TrimSpace(g.mustRun(dir, "rev-parse", "HEAD"))
	g.mustRun(dir, "checkout", "-q", "main")
	g.mustRun(dir, "push", "-qf", "origin", "HEAD~1:main")
	g.mustRun(dir, "push", "-q", "origin", ":feature")
	time.Sleep(20 * time.Millisecond)

	s3store := srv.deps.Storage.(*s3.S3Storage)
	res, err := s3store.Prune(repo.StorageKey, 0, 0, false)
	require.NoError(t, err)
	assert.Equal(t, 6, res.Pruned, "two commits with a blob and a tree each")

	st, err := srv.deps.Storage.GetStorer(repo.StorageKey)
	require.NoError(t, err)
	assert.Error(t, st.HasEncodedObject(plumbing.NewHash(dropped)))
	assert.Error(t, st.HasEncodedObject(plumbing.NewHash(feature)))

	check := t.TempDir()
	g.mustRun(check, "clone", "-q", g.url("owner/forced"), ".")
	assert.Equal(t, "3", strings.TrimSpace(g.mustRun(check, "rev-list", "--count", "HEAD")), "initial commit and two pushes")
	commitFile(t, g, check, "after.txt", "after prune")
	g.mustRun(check, "push", "-q", "origin", "HEAD")
	g.mustRun(t.TempDir(), "clone", "-q", g.url("owner/forced"), ".")
}
