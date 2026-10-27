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

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncPushMirrors runs every due push, as the workers would.
func (s *testServer) syncPushMirrors(t *testing.T) {
	t.Helper()
	for s.deps.PushMirrorApp.SyncDue(context.Background()) > 0 {
	}
}

// pushMirror returns a repository's only push mirror.
func (s *testServer) pushMirror(t *testing.T, repo, bearer string) map[string]any {
	t.Helper()
	status, body := s.get(t, repoAPI(repo)+"/push-mirrors", bearer)
	require.Equal(t, http.StatusOK, status, "%v", body)
	list := body["push_mirrors"].([]any)
	require.Len(t, list, 1)
	return list[0].(map[string]any)
}

func TestPushMirrors(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPushMirrors(t) })
	t.Run("s3_fake", func(t *testing.T) { testPushMirrors(t, withS3(s3test.StartFake(t))) })
}

func testPushMirrors(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, allowLoopbackMirrors)...)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Source", true)
	src := srv.url + "/alice/source.git"
	pushFiles(t, src, alice.git, "main", "one", map[string]string{"a.txt": "1\n"})
	pushFiles(t, src, alice.git, "feature", "f", map[string]string{"f.txt": "f\n"})
	pushTag(t, src, alice, "main", "v1.0.0")
	write := srv.createToken(t, bob.session, "mirror-write", "read:repository", "write:repository")

	t.Run("mirror", func(t *testing.T) {
		srv.createRepo(t, bob.session, "Target", true)
		target := srv.url + "/bob/target.git"
		pushFiles(t, target, bob.git, "extra", "only there", map[string]string{"x.txt": "x\n"})
		// Over postBuffer: the pack goes chunked.
		pushFiles(t, src, alice.git, "big", "big", map[string]string{"big.bin": string(randomBytes(t, 3<<20))})
		status, m := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": target, "username": write.Username, "password": write.Password})
		require.Equal(t, http.StatusOK, status, "%v", m)
		assert.Equal(t, "pending", m["status"])
		assert.Equal(t, "mirror", m["mode"], "the default")
		assert.Equal(t, true, m["has_password"])
		assert.NotContains(t, m, "password")

		srv.syncPushMirrors(t)
		m = srv.pushMirror(t, "alice/source", alice.session)
		require.Equal(t, "ok", m["status"], "%v", m)
		assert.Equal(t, srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/target"), "forced, and the remote's other branches deleted")

		// A push makes it due at once; a deleted branch goes there too.
		head := pushFiles(t, src, alice.git, "main", "two", map[string]string{"a.txt": "2\n"})
		deleteBranch(t, src, alice.git, "feature")
		srv.syncPushMirrors(t)
		refs := srv.refsOf(t, "bob/target")
		assert.Equal(t, head, refs["refs/heads/main"])
		assert.NotContains(t, refs, "refs/heads/feature")
		assert.Contains(t, refs, "refs/tags/v1.0.0")

		// Not due again before the interval.
		next, err := time.Parse(time.RFC3339Nano, srv.pushMirror(t, "alice/source", alice.session)["next_sync_at"].(string))
		require.NoError(t, err)
		assert.True(t, next.After(time.Now().Add(7*time.Hour)), "the default interval: %v", next)
		assert.Zero(t, srv.deps.PushMirrorApp.SyncDue(context.Background()))
	})

	t.Run("branches", func(t *testing.T) {
		srv.createRepo(t, bob.session, "Safe", true) // its main has a README: diverged
		target := srv.url + "/bob/safe.git"
		pushFiles(t, target, bob.git, "extra", "only there", map[string]string{"x.txt": "x\n"})
		pushFiles(t, src, alice.git, "feature", "again", map[string]string{"f.txt": "g\n"})
		status, m := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": target, "username": write.Username,
			"password": write.Password, "mode": "branches"})
		require.Equal(t, http.StatusOK, status, "%v", m)
		srv.syncPushMirrors(t)
		status, body := srv.get(t, repoAPI("alice/source")+"/push-mirrors", alice.session)
		require.Equal(t, http.StatusOK, status)
		var safe map[string]any
		for _, x := range body["push_mirrors"].([]any) {
			if x.(map[string]any)["mode"] == "branches" {
				safe = x.(map[string]any)
			}
		}
		require.NotNil(t, safe)
		assert.Equal(t, "failed", safe["status"])
		assert.Contains(t, safe["last_error"], "main: diverged")
		ours, theirs := srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/safe")
		assert.NotEqual(t, ours["refs/heads/main"], theirs["refs/heads/main"], "never forced")
		assert.Equal(t, ours["refs/heads/feature"], theirs["refs/heads/feature"], "the other refs still go")
		assert.Equal(t, ours["refs/tags/v1.0.0"], theirs["refs/tags/v1.0.0"])
		assert.Contains(t, theirs, "refs/heads/extra", "never deleted")
		next, err := time.Parse(time.RFC3339Nano, safe["next_sync_at"].(string))
		require.NoError(t, err)
		assert.True(t, next.After(time.Now().Add(4*time.Minute)) && next.Before(time.Now().Add(6*time.Minute)), "backoff: %v", next)

		status, _ = srv.request(t, http.MethodDelete, repoAPI("alice/source")+"/push-mirrors/"+safe["id"].(string), alice.session, nil)
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("pushed during a sync", func(t *testing.T) {
		path := "alice/source"
		repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
		require.NoError(t, err)
		id := srv.pushMirror(t, path, alice.session)["id"].(string)
		require.NoError(t, srv.deps.PushMirrorApp.Pers.Update(id, map[string]any{"next_sync_at": time.Now().UTC().Add(-time.Second)}))
		claimed, err := srv.deps.PushMirrorApp.Pers.Claim(time.Now().UTC(), time.Now().UTC().Add(time.Minute))
		require.NoError(t, err)
		require.NotNil(t, claimed)
		srv.deps.PushMirrorApp.Pushed(repo) // while it runs
		require.NoError(t, srv.deps.PushMirrorApp.Pers.Schedule(id, claimed.Generation, time.Now().UTC().Add(time.Hour)))
		require.NoError(t, srv.deps.PushMirrorApp.Pers.Update(id, map[string]any{"locked_until": nil}))
		assert.Equal(t, 1, srv.deps.PushMirrorApp.SyncDue(context.Background()), "still due: the push came after the sync started")
	})

	t.Run("pull mirror syncs go on", func(t *testing.T) {
		read := srv.createToken(t, alice.session, "mirror-read", "read:repository")
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Relay", "url": src, "username": read.Username,
			"password": read.Password, "mirror": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.createRepo(t, bob.session, "Far", true)
		status, m := srv.post(t, repoAPI("bob/relay")+"/push-mirrors", bob.session, map[string]any{"url": srv.url + "/bob/far.git",
			"username": write.Username, "password": write.Password})
		require.Equal(t, http.StatusOK, status, "%v", m)
		srv.syncMirrors(t)
		srv.syncPushMirrors(t)
		head := pushFiles(t, src, alice.git, "main", "three", map[string]string{"a.txt": "3\n"})
		status, _ = srv.post(t, repoAPI("bob/relay")+"/mirror/sync", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncMirrors(t)
		srv.syncPushMirrors(t)
		assert.Equal(t, head, srv.refsOf(t, "bob/far")["refs/heads/main"], "fetched, then pushed on")
	})

	t.Run("permissions and input", func(t *testing.T) {
		carol, erin := srv.newUser(t, "carol"), srv.newUser(t, "erin")
		for name, role := range map[string]string{"carol": "developer", "erin": "maintainer"} {
			status, _ := srv.post(t, repoAPI("alice/source")+"/members", alice.session, map[string]any{"username": name, "role": role})
			require.Equal(t, http.StatusOK, status)
		}
		status, _ := srv.get(t, repoAPI("alice/source")+"/push-mirrors", carol.session)
		assert.Equal(t, http.StatusForbidden, status, "developers don't see them")
		status, _ = srv.post(t, repoAPI("alice/source")+"/push-mirrors", carol.session, map[string]any{"url": "https://example.com/a.git"})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, repoAPI("alice/source")+"/push-mirrors", bob.session)
		assert.Equal(t, http.StatusNotFound, status, "a private repository stays hidden")
		id := srv.pushMirror(t, "alice/source", erin.session)["id"].(string)
		status, _ = srv.post(t, repoAPI("alice/source")+"/push-mirrors/"+id+"/sync", erin.session, nil)
		assert.Equal(t, http.StatusOK, status, "maintainers manage them")
		status, _ = srv.post(t, repoAPI("bob/target")+"/push-mirrors/"+id+"/sync", bob.session, nil)
		assert.Equal(t, http.StatusNotFound, status, "another repository's mirror")

		for _, bad := range []map[string]any{
			{"url": "file:///etc"}, {"url": "https://user:pw@example.com/a.git"}, {"url": "https://example.com/a.git", "mode": "force"},
			{"url": "https://example.com/a.git", "interval_seconds": 60}, {},
		} {
			status, _ = srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, bad)
			assert.Equal(t, http.StatusBadRequest, status, "%v", bad)
		}
		status, m := srv.request(t, http.MethodPatch, repoAPI("alice/source")+"/push-mirrors/"+id, alice.session, map[string]any{"mode": "branches", "interval_seconds": 3600})
		require.Equal(t, http.StatusOK, status, "%v", m)
		assert.Equal(t, "branches", m["mode"])
		assert.EqualValues(t, 3600, m["interval_seconds"])

		srv.createRepo(t, alice.session, "Many", true)
		for i := range 10 {
			status, _ = srv.post(t, repoAPI("alice/many")+"/push-mirrors", alice.session, map[string]any{"url": "https://example.com/" + strconv.Itoa(i) + ".git"})
			require.Equal(t, http.StatusOK, status)
		}
		status, body := srv.post(t, repoAPI("alice/many")+"/push-mirrors", alice.session, map[string]any{"url": "https://example.com/x.git"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "TOO_MANY_PUSH_MIRRORS", body["type"])

		// Deleting the repository takes its push mirrors.
		path := "alice/many"
		repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
		require.NoError(t, err)
		status, _ = srv.request(t, http.MethodDelete, repoAPI("alice/many")+"?confirm=many", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		left, err := srv.deps.PushMirrorApp.Pers.List(repo.Id)
		require.NoError(t, err)
		assert.Empty(t, left)
	})

	t.Run("ssh", func(t *testing.T) {
		port := srv.startSSH(t)
		srv.createRepo(t, bob.session, "Over SSH", true)
		status, m := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": "ssh://git@127.0.0.1:" + strconv.Itoa(port) + "/bob/over-ssh.git"})
		require.Equal(t, http.StatusOK, status, "%v", m)
		assert.Equal(t, "ssh", m["auth"])
		pub := m["ssh_public_key"].(string)
		require.True(t, strings.HasPrefix(pub, "ssh-ed25519 "), pub)
		id := m["id"].(string)
		get := func() map[string]any {
			_, body := srv.get(t, repoAPI("alice/source")+"/push-mirrors", alice.session)
			for _, x := range body["push_mirrors"].([]any) {
				if x.(map[string]any)["id"] == id {
					return x.(map[string]any)
				}
			}
			return nil
		}
		srv.syncPushMirrors(t)
		assert.Equal(t, "failed", get()["status"], "the key isn't on the remote yet")
		assert.NotEmpty(t, get()["ssh_host_key"])

		status, body := srv.post(t, repoAPI("bob/over-ssh")+"/deploy-keys", bob.session, map[string]any{"name": "mirror", "key": pub})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, repoAPI("alice/source")+"/push-mirrors/"+id+"/sync", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncPushMirrors(t)
		assert.Equal(t, "failed", get()["status"], "a read-only deploy key can't push")

		status, body = srv.get(t, repoAPI("bob/over-ssh")+"/deploy-keys", bob.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		keyId := body["keys"].([]any)[0].(map[string]any)["id"].(string)
		status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/over-ssh")+"/deploy-keys/"+keyId, bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, body = srv.post(t, repoAPI("bob/over-ssh")+"/deploy-keys", bob.session, map[string]any{"name": "mirror", "key": pub, "read_only": false})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, repoAPI("alice/source")+"/push-mirrors/"+id+"/sync", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.syncPushMirrors(t)
		require.Equal(t, "ok", get()["status"], "%v", get())
		assert.Equal(t, srv.refsOf(t, "alice/source"), srv.refsOf(t, "bob/over-ssh"))
	})
}

func TestPushMirrorsGuard(t *testing.T) {
	srv := startTestServer(t) // loopback not allowed
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Source", false)
	srv.createRepo(t, alice.session, "Target", false)
	status, body := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": srv.url + "/alice/target.git",
		"username": alice.git.Username, "password": alice.git.Password})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncPushMirrors(t)
	m := srv.pushMirror(t, "alice/source", alice.session)
	assert.Equal(t, "failed", m["status"])
	assert.Contains(t, m["last_error"], "private or local")
}

func TestPushMirrorsLFS(t *testing.T) {
	f := newPullFixture(t, true, allowLoopbackMirrors)
	srv := f.srv
	srv.deps.LFSApp.Config.Server.PublicURL = srv.url
	content := randomBytes(t, 4096)
	srv.upload(t, f.owner, "owner/app", content)
	pushFiles(t, f.url, f.owner.git, "main", "big file", map[string]string{"big.bin": pointer(content)})
	srv.createRepo(t, f.outer.session, "Copy", true)

	status, body := srv.post(t, repoAPI("owner/app")+"/push-mirrors", f.owner.session, map[string]any{"url": srv.url + "/outer/copy.git",
		"username": f.outer.git.Username, "password": f.outer.git.Password, "lfs": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncPushMirrors(t)
	m := srv.pushMirror(t, "owner/app", f.owner.session)
	require.Equal(t, "ok", m["status"], "%v", m)
	path := "outer/copy"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	size, ok, err := srv.deps.Storage.StatLFSObject(repo.StorageKey, oidOf(content))
	require.NoError(t, err)
	assert.True(t, ok, "the LFS object went along")
	assert.EqualValues(t, len(content), size)
}

// TestPushMirrorToRealGit pushes to git's own http-backend: an empty
// repository first, then incrementally, with real report-status and
// delete-refs.
func TestPushMirrorToRealGit(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPushMirrorToRealGit(t) })
	t.Run("s3_fake", func(t *testing.T) { testPushMirrorToRealGit(t, withS3(s3test.StartFake(t))) })
}

func testPushMirrorToRealGit(t *testing.T, opts ...func(*config.Config)) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	home := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	run(root, "init", "--bare", "-b", "main", "downstream.git")
	run(filepath.Join(root, "downstream.git"), "config", "http.receivepack", "true")
	backend := httptest.NewServer(&cgi.Handler{Path: gitBin, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	t.Cleanup(backend.Close)
	bare := filepath.Join(root, "downstream.git")

	srv := startTestServer(t, append(opts, allowLoopbackMirrors)...)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Source", false)
	src := srv.url + "/alice/source.git"
	for i := range 20 {
		pushFiles(t, src, alice.git, "main", "c"+strconv.Itoa(i), map[string]string{"f.txt": strconv.Itoa(i) + "\n"})
	}
	pushFiles(t, src, alice.git, "feature", "f", map[string]string{"g.txt": "g\n"})
	pushTag(t, src, alice, "main", "v1")

	status, body := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": backend.URL + "/downstream.git"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncPushMirrors(t)
	m := srv.pushMirror(t, "alice/source", alice.session)
	require.Equal(t, "ok", m["status"], "%v", m)
	refs := srv.refsOf(t, "alice/source")
	assert.Equal(t, refs["refs/heads/main"], run(bare, "rev-parse", "main"))
	assert.Equal(t, refs["refs/heads/feature"], run(bare, "rev-parse", "feature"))
	assert.Equal(t, refs["refs/tags/v1"], run(bare, "rev-parse", "v1"))
	run(bare, "fsck", "--strict")

	// Incremental, plus a deletion.
	head := pushFiles(t, src, alice.git, "main", "more", map[string]string{"f.txt": "more\n"})
	deleteBranch(t, src, alice.git, "feature")
	srv.syncPushMirrors(t)
	require.Equal(t, "ok", srv.pushMirror(t, "alice/source", alice.session)["status"])
	assert.Equal(t, head, run(bare, "rev-parse", "main"))
	assert.NotContains(t, run(bare, "branch", "--list"), "feature")
	run(bare, "fsck", "--strict")
}
