package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

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
			g.mustRun(writer, "clone", "-q", g.url("big"), ".")
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
			measure("clone", func() { g.mustRun(reader, "clone", "-q", g.url("big"), ".") })
			assert.Positive(t, counter.Get("RANGE"), "objects are read from packs with ranged GETs")
			commitFile(t, g, writer, "x.txt", "x")
			measure("push 1 commit", func() { g.mustRun(writer, "push", "-q", "origin", "HEAD") })
			measure("fetch 1 commit", func() { g.mustRun(reader, "fetch", "-q", "origin") })
			measure("clone over HTTP", func() { g.mustRun(t.TempDir(), "clone", "-q", srv.url+"/big.git", ".") })
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
	g.mustRun(dir, "clone", "-q", g.url("old"), ".")
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
	g.mustRun(check, "clone", "-q", g.url("old"), ".")
	assert.FileExists(t, filepath.Join(check, "new.txt"), "both the old and the pushed history survive")
	assert.LessOrEqual(t, counter.Total(), requestBudget, "and the clone costs a handful of requests: %v", counter)
}
