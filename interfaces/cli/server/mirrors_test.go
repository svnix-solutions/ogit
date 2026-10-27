package server

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allowLoopbackMirrors lets mirrors fetch from the test server itself.
func allowLoopbackMirrors(c *config.Config) { c.Mirrors.AllowedHosts = []string{"127.0.0.1"} }

// syncMirrors runs every due sync, as the worker would.
func (s *testServer) syncMirrors(t *testing.T) {
	t.Helper()
	for s.deps.MirrorApp.SyncDue(context.Background()) > 0 {
	}
}

// branches lists a repository's branches and tags: name -> hash.
func (s *testServer) refsOf(t *testing.T, repo string) map[string]string {
	t.Helper()
	r, err := s.deps.RepoApp.GetRepository(nil, &repo)
	require.NoError(t, err)
	st, err := s.deps.Storage.GetStorer(r.StorageKey)
	require.NoError(t, err)
	out := map[string]string{}
	iter, err := st.IterReferences()
	require.NoError(t, err)
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() || ref.Name().IsTag() {
			out[ref.Name().String()] = ref.Hash().String()
		}
		return nil
	})
	return out
}

func TestMirrors(t *testing.T) {
	t.Run("local", func(t *testing.T) { testMirrors(t) })
	t.Run("s3_fake", func(t *testing.T) { testMirrors(t, withS3(s3test.StartFake(t))) })
}

func testMirrors(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, allowLoopbackMirrors)...)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Source", true)
	src := srv.url + "/alice/source.git"
	pushFiles(t, src, alice.git, "main", "one", map[string]string{"a.txt": "1\n"})
	pushFiles(t, src, alice.git, "feature", "f", map[string]string{"f.txt": "f\n"})
	pushTag(t, src, alice, "main", "v1.0.0")
	read := srv.createToken(t, alice.session, "mirror-read", "read:repository")

	t.Run("import", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": src, "username": read.Username, "password": read.Password})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "pending", body["mirror"].(map[string]any)["status"])
		assert.Equal(t, false, body["repository"].(map[string]any)["is_mirror"])
		srv.syncMirrors(t)
		assert.Equal(t, srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/copy"), "branches and tags")
		status, _ = srv.get(t, repoAPI("bob/copy")+"/mirror", bob.session)
		assert.Equal(t, http.StatusNotFound, status, "an import is a normal repository once done")
		pushFiles(t, srv.url+"/bob/copy.git", bob.git, "main", "mine", map[string]string{"b.txt": "b\n"})
	})

	t.Run("mirror", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Mirror", "url": src, "username": read.Username,
			"password": read.Password, "mirror": true, "interval_seconds": 3600})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.syncMirrors(t)
		status, m := srv.get(t, repoAPI("bob/mirror")+"/mirror", bob.session)
		require.Equal(t, http.StatusOK, status, "%v", m)
		assert.Equal(t, "ok", m["status"])
		assert.Equal(t, true, m["has_password"])
		assert.NotContains(t, m, "password")
		assert.Equal(t, srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/mirror"))
		status, repo := srv.get(t, repoAPI("bob/mirror"), bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, repo["is_mirror"])

		err := tryPushFiles(t, srv.url+"/bob/mirror.git", bob.git, "main", map[string]string{"x.txt": "x\n"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mirror", "pushes are refused")

		// The remote moves: a new commit, a deleted branch.
		head := pushFiles(t, src, alice.git, "main", "two", map[string]string{"a.txt": "2\n"})
		deleteBranch(t, src, alice.git, "feature")
		srv.syncMirrors(t)
		assert.NotEqual(t, head, srv.refsOf(t, "bob/mirror")["refs/heads/main"], "not yet: the interval")
		status, _ = srv.post(t, repoAPI("bob/mirror")+"/mirror/sync", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncMirrors(t)
		refs := srv.refsOf(t, "bob/mirror")
		assert.Equal(t, head, refs["refs/heads/main"])
		assert.NotContains(t, refs, "refs/heads/feature", "deleted upstream")
		assert.Contains(t, refs, "refs/tags/v1.0.0")
	})

	t.Run("failures", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Bad", "url": src, "username": read.Username, "password": "ogit_" + strings.Repeat("0", 64), "mirror": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.syncMirrors(t)
		_, m := srv.get(t, repoAPI("bob/bad")+"/mirror", bob.session)
		assert.Equal(t, "failed", m["status"])
		assert.Contains(t, m["last_error"], "authentication failed")
		next, err := time.Parse(time.RFC3339Nano, m["next_sync_at"].(string))
		require.NoError(t, err)
		assert.True(t, next.After(time.Now().Add(4*time.Minute)), "backoff")

		status, m = srv.request(t, http.MethodPatch, repoAPI("bob/bad")+"/mirror", bob.session, map[string]any{"password": read.Password})
		require.Equal(t, http.StatusOK, status, "%v", m)
		srv.syncMirrors(t)
		_, m = srv.get(t, repoAPI("bob/bad")+"/mirror", bob.session)
		assert.Equal(t, "ok", m["status"], "new credentials sync at once: %v", m)

		for _, bad := range []map[string]any{
			{"name": "X", "url": "file:///etc"}, {"name": "X", "url": "https://user:pw@example.com/a.git"}, {"name": "X", "url": "git://example.com/a"},
			{"name": "X", "url": src, "mirror": true, "interval_seconds": 60},
		} {
			status, _ = srv.post(t, "/api/v1/repository/import", bob.session, bad)
			assert.Equal(t, http.StatusBadRequest, status, "%v", bad)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		carol := srv.newUser(t, "carol")
		status, _ := srv.post(t, repoAPI("bob/mirror")+"/members", bob.session, map[string]any{"username": "carol", "role": "developer"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, repoAPI("bob/mirror")+"/mirror", carol.session)
		assert.Equal(t, http.StatusForbidden, status, "developers don't see the mirror's settings")
		status, _ = srv.post(t, repoAPI("bob/mirror")+"/mirror/sync", carol.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.post(t, "/api/v1/repository/import", carol.session, map[string]any{"namespace": "bob", "name": "Theirs", "url": src})
		assert.Equal(t, http.StatusForbidden, status, "only where you may create repositories")

		erin := srv.newUser(t, "erin")
		status, _ = srv.post(t, repoAPI("bob/mirror")+"/members", bob.session, map[string]any{"username": "erin", "role": "maintainer"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, repoAPI("bob/mirror")+"/mirror", erin.session)
		assert.Equal(t, http.StatusOK, status, "maintainers see it")
		status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/mirror")+"/mirror", erin.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "only owners stop mirroring")
		status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/mirror")+"/mirror", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		pushFiles(t, srv.url+"/bob/mirror.git", bob.git, "main", "free", map[string]string{"x.txt": "x\n"})
	})

	t.Run("ssh", func(t *testing.T) {
		port := srv.startSSH(t)
		url := "ssh://git@127.0.0.1:" + strconv.Itoa(port) + "/alice/source.git"
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Over SSH", "url": url, "mirror": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		m := body["mirror"].(map[string]any)
		assert.Equal(t, "ssh", m["auth"])
		pub := m["ssh_public_key"].(string)
		require.True(t, strings.HasPrefix(pub, "ssh-ed25519 "), pub)

		srv.syncMirrors(t)
		_, m = srv.get(t, repoAPI("bob/over-ssh")+"/mirror", bob.session)
		assert.Equal(t, "failed", m["status"], "the key isn't on the remote yet")
		assert.NotEmpty(t, m["ssh_host_key"], "the host key is pinned at once")

		status, body = srv.post(t, repoAPI("alice/source")+"/deploy-keys", alice.session, map[string]any{"name": "mirror", "key": pub})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, repoAPI("bob/over-ssh")+"/mirror/sync", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncMirrors(t)
		_, m = srv.get(t, repoAPI("bob/over-ssh")+"/mirror", bob.session)
		assert.Equal(t, "ok", m["status"], "%v", m)
		assert.Equal(t, srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/over-ssh"))

		// Another host key: refused.
		require.NoError(t, srv.deps.MirrorApp.Pers.Update(mirrorId(t, srv, "bob/over-ssh"), map[string]any{"ssh_host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}))
		status, _ = srv.post(t, repoAPI("bob/over-ssh")+"/mirror/sync", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncMirrors(t)
		_, m = srv.get(t, repoAPI("bob/over-ssh")+"/mirror", bob.session)
		assert.Equal(t, "failed", m["status"])
		assert.Contains(t, m["last_error"], "host key changed")
	})
}

// deleteBranch deletes a branch with a push.
func deleteBranch(t *testing.T, url string, auth *githttp.BasicAuth, branch string) {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: auth})
	require.NoError(t, err)
	require.NoError(t, repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(":refs/heads/" + branch)}}))
}

func mirrorId(t *testing.T, srv *testServer, path string) string {
	t.Helper()
	r, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	m, err := srv.deps.MirrorApp.Pers.ForRepository(r.Id)
	require.NoError(t, err)
	return m.Id
}

func TestMirrorsGuard(t *testing.T) {
	srv := startTestServer(t) // loopback not allowed
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Source", false)
	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": srv.url + "/alice/source.git", "mirror": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)
	_, m := srv.get(t, repoAPI("bob/copy")+"/mirror", bob.session)
	assert.Equal(t, "failed", m["status"])
	assert.Contains(t, m["last_error"], "private or local")
	assert.Empty(t, srv.refsOf(t, "bob/copy"))

	// Over SSH too (go-git dials the address the guard resolved).
	port := srv.startSSH(t)
	status, body = srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Over SSH", "mirror": true,
		"url": "ssh://git@127.0.0.1:" + strconv.Itoa(port) + "/alice/source.git"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)
	_, m = srv.get(t, repoAPI("bob/over-ssh")+"/mirror", bob.session)
	assert.Equal(t, "failed", m["status"])
	assert.Contains(t, m["last_error"], "private or local")
	assert.Empty(t, m["ssh_host_key"], "never connected")
}

func TestMirrorsWithoutEncryptionKey(t *testing.T) {
	srv := startTestServer(t, allowLoopbackMirrors, func(c *config.Config) { c.Security.EncryptionKey = "" })
	bob := srv.newUser(t, "bob")
	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": "https://example.com/a.git", "password": "secret"})
	assert.Equal(t, http.StatusConflict, status, "%v", body)
	assert.Equal(t, "ENCRYPTION_KEY_REQUIRED", body["type"])
	status, _ = srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Public", "url": "https://example.com/a.git"})
	assert.Equal(t, http.StatusOK, status, "anonymous remotes need no key")
	_ = domain.MirrorPending
}

func TestMirrorsLFS(t *testing.T) {
	f := newPullFixture(t, true, allowLoopbackMirrors)
	srv := f.srv
	srv.deps.LFSApp.Config.Server.PublicURL = srv.url
	content := randomBytes(t, 4096)
	srv.upload(t, f.owner, "owner/app", content)
	pushFiles(t, f.url, f.owner.git, "main", "big file", map[string]string{"big.bin": pointer(content)})
	read := srv.createToken(t, f.owner.session, "lfs-read", "read:repository")

	status, body := srv.post(t, "/api/v1/repository/import", f.outer.session, map[string]any{"name": "Copy", "url": f.url, "username": read.Username, "password": read.Password, "lfs": true, "mirror": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)
	_, m := srv.get(t, repoAPI("outer/copy")+"/mirror", f.outer.session)
	require.Equal(t, "ok", m["status"], "%v", m)
	path := "outer/copy"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	size, ok, err := srv.deps.Storage.StatLFSObject(repo.StorageKey, oidOf(content))
	require.NoError(t, err)
	assert.True(t, ok, "the LFS object came along")
	assert.EqualValues(t, len(content), size)
}

// TestMirrorFromRealGit mirrors a repository served by git's own
// http-backend (what GitHub, GitLab and others run), as the first sync and
// incrementally, with real negotiation and side-band.
func TestMirrorFromRealGit(t *testing.T) {
	t.Run("local", func(t *testing.T) { testMirrorFromRealGit(t) })
	t.Run("s3_fake", func(t *testing.T) { testMirrorFromRealGit(t, withS3(s3test.StartFake(t))) })
}

func testMirrorFromRealGit(t *testing.T, opts ...func(*config.Config)) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	work := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+work, "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run(root, "init", "--bare", "-b", "trunk", "upstream.git")
	run(work, "init", "-b", "trunk", ".")
	for i := range 30 {
		require.NoError(t, os.WriteFile(filepath.Join(work, "f.txt"), []byte(strconv.Itoa(i)+"\n"), 0o644))
		run(work, "add", ".")
		run(work, "commit", "-qm", "c"+strconv.Itoa(i))
	}
	run(work, "tag", "-a", "v1", "-m", "release")
	run(work, "push", "-q", filepath.Join(root, "upstream.git"), "trunk", "v1")

	backend := httptest.NewServer(&cgi.Handler{Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(backend.Close)

	srv := startTestServer(t, append(opts, allowLoopbackMirrors)...)
	bob := srv.newUser(t, "bob")
	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": backend.URL + "/upstream.git", "mirror": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)
	_, m := srv.get(t, repoAPI("bob/copy")+"/mirror", bob.session)
	require.Equal(t, "ok", m["status"], "%v", m)
	refs := srv.refsOf(t, "bob/copy")
	assert.Equal(t, run(root+"/upstream.git", "rev-parse", "trunk"), refs["refs/heads/trunk"])
	assert.Equal(t, run(root+"/upstream.git", "rev-parse", "v1"), refs["refs/tags/v1"], "the annotated tag object")
	copyPath := "bob/copy"
	copyRepo, err := srv.deps.RepoApp.GetRepository(nil, &copyPath)
	require.NoError(t, err)
	assert.Equal(t, "trunk", copyRepo.DefaultBranch, "HEAD follows the remote")

	// More commits upstream: an incremental fetch (haves negotiated).
	for i := range 5 {
		require.NoError(t, os.WriteFile(filepath.Join(work, "g.txt"), []byte(strconv.Itoa(i)+"\n"), 0o644))
		run(work, "add", ".")
		run(work, "commit", "-qm", "d"+strconv.Itoa(i))
	}
	run(work, "push", "-q", filepath.Join(root, "upstream.git"), "trunk")
	status, _ = srv.post(t, repoAPI("bob/copy")+"/mirror/sync", bob.session, nil)
	require.Equal(t, http.StatusOK, status)
	srv.syncMirrors(t)
	assert.Equal(t, run(root+"/upstream.git", "rev-parse", "trunk"), srv.refsOf(t, "bob/copy")["refs/heads/trunk"])
	_, err = clone(t, srv.url+"/bob/copy.git", bob.git)
	assert.NoError(t, err, "the mirror is a complete repository")
}
