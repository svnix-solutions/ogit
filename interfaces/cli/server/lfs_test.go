package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"time"

	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lfsCLI is the real git with git-lfs, its filters installed in an isolated
// HOME.
func (s *testServer) lfsCLI(t *testing.T, as user) *gitCLI {
	t.Helper()
	if _, err := exec.LookPath("git-lfs"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("git-lfs not installed; the LFS tests need the real client")
		}
		t.Skip("git-lfs not installed")
	}
	port := s.startSSH(t)
	key := newSSHKey(t, as.name+"@lfs")
	status, body := s.post(t, "/api/v1/user/ssh-keys", as.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)
	g.mustRun(g.home, "lfs", "install", "--skip-repo")
	// Links in the batch API and from git-lfs-authenticate.
	s.deps.LFSApp.Config.Server.PublicURL = s.url
	return g
}

// httpURL is a repository's HTTP URL with as's access token in it.
func (s *testServer) httpURL(as user, path string) string {
	u, _ := url.Parse(s.url + "/" + path + ".git")
	u.User = url.UserPassword(as.git.Username, as.git.Password)
	return u.String()
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

func oidOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestGitLFS(t *testing.T) {
	t.Run("local", func(t *testing.T) { testGitLFS(t) })
	t.Run("s3_fake", func(t *testing.T) { testGitLFS(t, withS3(s3test.StartFake(t))) })
}

func testGitLFS(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Media", true)
	g := srv.lfsCLI(t, owner)
	big := randomBytes(t, 5<<20) // over the 4 MB body limit of other routes

	a := t.TempDir()
	g.mustRun(a, "clone", srv.httpURL(owner, "owner/media"), ".")
	g.mustRun(a, "lfs", "track", "*.bin")
	require.NoError(t, os.WriteFile(filepath.Join(a, "big.bin"), big, 0o644))
	g.mustRun(a, "add", ".gitattributes", "big.bin")
	g.mustRun(a, "commit", "-qm", "add big.bin")
	g.mustRun(a, "push", "origin", "HEAD")

	repo, err := srv.deps.RepoApp.GetRepository(nil, ptr("owner/media"))
	require.NoError(t, err)
	size, ok, err := srv.deps.Storage.StatLFSObject(repo.StorageKey, oidOf(big))
	require.NoError(t, err)
	assert.True(t, ok, "stored")
	assert.Equal(t, int64(len(big)), size)

	t.Run("clone_over_http", func(t *testing.T) {
		b := t.TempDir()
		g.mustRun(b, "clone", srv.httpURL(owner, "owner/media"), ".")
		got, err := os.ReadFile(filepath.Join(b, "big.bin"))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(big, got), "the content, not the pointer")
	})

	t.Run("over_ssh", func(t *testing.T) {
		b := t.TempDir()
		g.mustRun(b, "clone", g.url("owner/media"), ".")
		got, err := os.ReadFile(filepath.Join(b, "big.bin"))
		require.NoError(t, err)
		assert.True(t, bytes.Equal(big, got))

		more := randomBytes(t, 1<<20)
		require.NoError(t, os.WriteFile(filepath.Join(b, "more.bin"), more, 0o644))
		g.mustRun(b, "add", "more.bin")
		g.mustRun(b, "commit", "-qm", "more")
		g.mustRun(b, "push", "origin", "HEAD")
		_, ok, err := srv.deps.Storage.StatLFSObject(repo.StorageKey, oidOf(more))
		require.NoError(t, err)
		assert.True(t, ok, "uploaded over SSH")
		g.mustRun(a, "pull", "-q", "origin", "main")
	})

	t.Run("locks_with_the_client", func(t *testing.T) {
		assert.Contains(t, g.mustRun(a, "lfs", "lock", "big.bin"), "Locked big.bin")
		out := g.mustRun(a, "lfs", "locks")
		assert.Contains(t, out, "big.bin")
		assert.Contains(t, out, "owner")
		g.mustRun(a, "lfs", "unlock", "big.bin")
		assert.NotContains(t, g.mustRun(a, "lfs", "locks"), "big.bin")
	})

	t.Run("web_raw_and_archives", func(t *testing.T) {
		missing := randomBytes(t, 100)
		pushFiles(t, srv.url+"/owner/media.git", owner.git, "main", "a pointer without its object", map[string]string{"missing.bin": pointer(missing)})

		status, body := srv.get(t, repoAPI("owner/media")+"/blob?spec=main/big.bin", owner.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		lfs, _ := body["file"].(map[string]any)["lfs"].(map[string]any)
		assert.Equal(t, map[string]any{"oid": oidOf(big), "size": float64(len(big)), "present": true}, lfs)
		_, body = srv.get(t, repoAPI("owner/media")+"/blob?spec=main/missing.bin", owner.session)
		assert.Equal(t, false, body["file"].(map[string]any)["lfs"].(map[string]any)["present"])
		_, body = srv.get(t, repoAPI("owner/media")+"/blob?spec=main/README.md", owner.session)
		assert.NotContains(t, body["file"], "lfs")

		status, header, got := fetch(t, srv.url+"/owner/media/raw/main/big.bin", basic(owner.git))
		require.Equal(t, http.StatusOK, status)
		assert.True(t, bytes.Equal(big, got), "the content")
		assert.Equal(t, "application/octet-stream", header.Get("Content-Type"))
		_, _, got = fetch(t, srv.url+"/owner/media/raw/main/missing.bin", basic(owner.git))
		assert.Equal(t, pointer(missing), string(got), "the pointer, without its object")

		status, _, archive := fetch(t, srv.url+"/owner/media/archive/main.zip", basic(owner.git))
		require.Equal(t, http.StatusOK, status)
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		require.NoError(t, err)
		files := map[string][]byte{}
		for _, f := range zr.File {
			r, err := f.Open()
			require.NoError(t, err)
			files[f.Name], _ = io.ReadAll(r)
			r.Close()
		}
		assert.True(t, bytes.Equal(big, files["media-main/big.bin"]), "archives have the content")
		assert.Equal(t, pointer(missing), string(files["media-main/missing.bin"]))
	})

	t.Run("anonymous_on_private", func(t *testing.T) {
		status, _, _ := srv.do(t, http.MethodGet, "/owner/media.git/info/lfs/objects/"+oidOf(big), nil, nil)
		assert.Equal(t, http.StatusUnauthorized, status)
	})
}

// lfsDo calls the LFS API, with as's access token unless as is zero.
func (s *testServer) lfsDo(t *testing.T, as user, method, path string, body any) (int, map[string]any) {
	t.Helper()
	h := map[string]string{"Accept": "application/vnd.git-lfs+json"}
	if as.git != nil {
		h = basic(as.git)
	}
	status, resp, _ := s.do(t, method, path, h, body)
	return status, resp
}

func lfsPath(repo, rest string) string { return "/" + repo + ".git/info/lfs/" + rest }

// lfsSend sends raw bytes (uploads) and returns the status and the body.
func (s *testServer) lfsSend(t *testing.T, method, path string, headers map[string]string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, bytes.NewReader(body))
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func batch(op string, objects ...map[string]any) map[string]any {
	return map[string]any{"operation": op, "transfers": []string{"basic"}, "objects": objects}
}

func lfsObject(b []byte) map[string]any { return map[string]any{"oid": oidOf(b), "size": len(b)} }

// action returns a batch answer's action for its only object.
func action(t *testing.T, resp map[string]any, name string) (href string, header map[string]string) {
	t.Helper()
	objects, _ := resp["objects"].([]any)
	require.Len(t, objects, 1, "%v", resp)
	actions, _ := objects[0].(map[string]any)["actions"].(map[string]any)
	a, ok := actions[name].(map[string]any)
	if !ok {
		return "", nil
	}
	header = map[string]string{}
	for k, v := range a["header"].(map[string]any) {
		header[k] = v.(string)
	}
	return a["href"].(string), header
}

func objectError(resp map[string]any) map[string]any {
	objects, _ := resp["objects"].([]any)
	if len(objects) != 1 {
		return nil
	}
	e, _ := objects[0].(map[string]any)["error"].(map[string]any)
	return e
}

func pointer(b []byte) string {
	return fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oidOf(b), len(b))
}

// upload stores content in repo as as, through the batch API.
func (s *testServer) upload(t *testing.T, as user, repo string, content []byte) {
	t.Helper()
	status, resp := s.lfsDo(t, as, http.MethodPost, lfsPath(repo, "objects/batch"), batch("upload", lfsObject(content)))
	require.Equal(t, http.StatusOK, status, "%v", resp)
	href, header := action(t, resp, "upload")
	require.NotEmpty(t, href, "%v", resp)
	status, out := s.lfsSend(t, http.MethodPut, strings.TrimPrefix(href, s.url), header, content)
	require.Equal(t, http.StatusOK, status, "%s", out)
}

func TestLFSObjects(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.LFS.MaxObjectSize = 1 << 20 })
	srv := f.srv
	srv.deps.LFSApp.Config.Server.PublicURL = srv.url
	content := randomBytes(t, 4096)
	objects := lfsPath("owner/app", "objects/")

	t.Run("upload", func(t *testing.T) {
		status, resp := srv.lfsDo(t, f.reporter, http.MethodPost, objects+"batch", batch("upload", lfsObject(content)))
		assert.Equal(t, http.StatusForbidden, status, "reporters don't upload: %v", resp)
		status, resp = srv.lfsDo(t, user{}, http.MethodPost, objects+"batch", batch("upload", lfsObject(content)))
		assert.Equal(t, http.StatusUnauthorized, status, "%v", resp)

		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, objects+"batch", batch("upload", lfsObject(content)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, header := action(t, resp, "upload")
		assert.Equal(t, srv.url+objects+oidOf(content), href)
		assert.True(t, strings.HasPrefix(header["Authorization"], "Bearer "))
		verify, _ := action(t, resp, "verify")
		assert.Equal(t, srv.url+objects+"verify", verify)

		status, out := srv.lfsSend(t, http.MethodPut, objects+oidOf(content), header, content[:100])
		assert.Equal(t, http.StatusUnprocessableEntity, status, "a truncated upload: %s", out)
		// The first byte flipped (setting it could leave random content
		// unchanged).
		status, out = srv.lfsSend(t, http.MethodPut, objects+oidOf(content), header, append([]byte{content[0] ^ 0xff}, content[1:]...))
		assert.Equal(t, http.StatusUnprocessableEntity, status, "other content: %s", out)
		status, resp, _ = srv.do(t, http.MethodPost, objects+"verify", header, lfsObject(content))
		assert.Equal(t, http.StatusNotFound, status, "nothing was kept: %v", resp)

		status, out = srv.lfsSend(t, http.MethodPut, objects+oidOf(content), header, content)
		require.Equal(t, http.StatusOK, status, "%s", out)
		status, resp, _ = srv.do(t, http.MethodPost, objects+"verify", header, lfsObject(content))
		assert.Equal(t, http.StatusOK, status, "%v", resp)

		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, objects+"batch", batch("upload", lfsObject(content)))
		require.Equal(t, http.StatusOK, status)
		href, _ = action(t, resp, "upload")
		assert.Empty(t, href, "already there")
	})

	t.Run("download", func(t *testing.T) {
		status, resp := srv.lfsDo(t, f.reporter, http.MethodPost, objects+"batch", batch("download", lfsObject(content)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, header := action(t, resp, "download")
		status, _, got := fetch(t, href, header)
		require.Equal(t, http.StatusOK, status)
		assert.True(t, bytes.Equal(content, got))

		status, out := srv.lfsSend(t, http.MethodPut, objects+oidOf(content), header, content)
		assert.Equal(t, http.StatusForbidden, status, "a download token doesn't upload: %s", out)

		status, _, got = fetch(t, srv.url+objects+oidOf(content), basic(f.reporter.git))
		assert.Equal(t, http.StatusOK, status, "with an access token too")
		assert.True(t, bytes.Equal(content, got))
		status, _, _ = fetch(t, srv.url+objects+oidOf(content), basic(f.outer.git))
		assert.Equal(t, http.StatusNotFound, status, "private")

		missing := randomBytes(t, 10)
		status, resp = srv.lfsDo(t, f.reporter, http.MethodPost, objects+"batch", batch("download", lfsObject(missing)))
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 404, objectError(resp)["code"], "%v", resp)
	})

	t.Run("tokens", func(t *testing.T) {
		srv.createRepo(t, f.owner.session, "Other", true)
		status, resp := srv.lfsDo(t, f.owner, http.MethodPost, lfsPath("owner/other", "objects/batch"), batch("upload", lfsObject(content)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		_, header := action(t, resp, "upload")
		status, out := srv.lfsSend(t, http.MethodGet, objects+oidOf(content), header, nil)
		assert.Equal(t, http.StatusNotFound, status, "another repository's token: %s", out)
		header["Authorization"] += "x"
		status, out = srv.lfsSend(t, http.MethodPut, lfsPath("owner/other", "objects/")+oidOf(content), header, content)
		assert.Equal(t, http.StatusUnauthorized, status, "%s", out)
	})

	t.Run("tokens_follow_access", func(t *testing.T) {
		gone := srv.newUser(t, "gone")
		status, body := srv.post(t, repoAPI("owner/app")+"/members", f.owner.session, map[string]any{"username": "gone", "role": "developer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, resp := srv.lfsDo(t, gone, http.MethodPost, objects+"batch", batch("download", lfsObject(content)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, header := action(t, resp, "download")
		status, _, _ = fetch(t, href, header)
		require.Equal(t, http.StatusOK, status)

		members := srv.members(t, "owner/app", f.owner)
		status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"/members/"+members["gone"]["id"].(string), f.owner.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _, _ = fetch(t, href, header)
		assert.Equal(t, http.StatusNotFound, status, "the token doesn't outlive the membership")
	})

	t.Run("invalid_and_too_large", func(t *testing.T) {
		status, resp := srv.lfsDo(t, f.dev, http.MethodPost, objects+"batch", batch("upload", map[string]any{"oid": "../../etc", "size": 1}))
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 422, objectError(resp)["code"], "%v", resp)

		large := randomBytes(t, 1<<20+1)
		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, objects+"batch", batch("upload", lfsObject(large)))
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 422, objectError(resp)["code"], "%v", resp)
		assert.Contains(t, objectError(resp)["message"], "lfs.max_object_size")
		status, out := srv.lfsSend(t, http.MethodPut, objects+oidOf(large), basic(f.dev.git), large)
		assert.Equal(t, http.StatusRequestEntityTooLarge, status, "%s", out)
		// Without a length (chunked), the limit is counted while streaming.
		req, err := http.NewRequest(http.MethodPut, srv.url+objects+oidOf(large), io.MultiReader(bytes.NewReader(large)))
		require.NoError(t, err)
		req.Header.Set("Authorization", basic(f.dev.git)["Authorization"])
		req.ContentLength = -1
		resp2, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp2.Body.Close()
		assert.Equal(t, http.StatusRequestEntityTooLarge, resp2.StatusCode, "chunked")
		repo, err := srv.deps.RepoApp.GetRepository(nil, ptr("owner/app"))
		require.NoError(t, err)
		_, ok, err := srv.deps.Storage.StatLFSObject(repo.StorageKey, oidOf(large))
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("forks", func(t *testing.T) {
		srv.fork(t, f.reporter, "owner/app", map[string]any{})
		forkObjects := lfsPath("reporter/app", "objects/")
		status, _, got := fetch(t, srv.url+forkObjects+oidOf(content), basic(f.reporter.git))
		require.Equal(t, http.StatusOK, status, "from the original")
		assert.True(t, bytes.Equal(content, got))

		// Uploads complete the fork itself.
		status, resp := srv.lfsDo(t, f.reporter, http.MethodPost, forkObjects+"batch", batch("upload", lfsObject(content)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, _ := action(t, resp, "upload")
		assert.NotEmpty(t, href)

		status, body := srv.post(t, repoAPI("reporter/app")+"/members", f.reporter.session, map[string]any{"username": "outer", "role": "reporter"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _, _ = fetch(t, srv.url+forkObjects+oidOf(content), basic(f.outer.git))
		assert.Equal(t, http.StatusNotFound, status, "outer can't read the original")

		srv.upload(t, f.reporter, "reporter/app", content)
		status, _, _ = fetch(t, srv.url+forkObjects+oidOf(content), basic(f.outer.git))
		assert.Equal(t, http.StatusOK, status, "now the fork has it")
	})
}

// tryPushFiles is pushFiles that returns the push error.
func tryPushFiles(t *testing.T, repoURL string, auth *githttp.BasicAuth, branch string, files map[string]string, remove ...string) error {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: repoURL, Auth: auth})
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	if branch != "main" {
		require.NoError(t, wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch), Create: true}))
	}
	for name, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		_, err := wt.Add(name)
		require.NoError(t, err)
	}
	for _, name := range remove {
		_, err := wt.Remove(name)
		require.NoError(t, err)
	}
	_, err = wt.Commit("change", &git.CommitOptions{AllowEmptyCommits: true, Author: &object.Signature{Name: "A", Email: "a@example.com", When: time.Now()}})
	require.NoError(t, err)
	refspec := gitconfig.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	return repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{refspec}})
}

func TestLFSLocks(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	locks := lfsPath("owner/app", "locks")
	pushFiles(t, f.url, f.owner.git, "main", "art", map[string]string{"art/logo.psd": "v1\n", "free.txt": "v1\n"})

	var lockId string
	t.Run("lock", func(t *testing.T) {
		status, resp := srv.lfsDo(t, f.reporter, http.MethodPost, locks, map[string]any{"path": "art/logo.psd"})
		assert.Equal(t, http.StatusForbidden, status, "reporters can't lock: %v", resp)
		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, locks, map[string]any{"path": "art/logo.psd", "ref": map[string]any{"name": "refs/heads/main"}})
		require.Equal(t, http.StatusCreated, status, "%v", resp)
		lock := resp["lock"].(map[string]any)
		lockId = lock["id"].(string)
		assert.Equal(t, "art/logo.psd", lock["path"])
		assert.Equal(t, "dev", lock["owner"].(map[string]any)["name"])

		status, resp = srv.lfsDo(t, f.maint, http.MethodPost, locks, map[string]any{"path": "/art/./logo.psd"})
		assert.Equal(t, http.StatusConflict, status, "%v", resp)
		assert.Equal(t, lockId, resp["lock"].(map[string]any)["id"])
		status, resp = srv.lfsDo(t, f.maint, http.MethodPost, locks, map[string]any{"path": ""})
		assert.Equal(t, http.StatusUnprocessableEntity, status, "%v", resp)
	})

	t.Run("list_and_verify", func(t *testing.T) {
		status, resp := srv.lfsDo(t, f.reporter, http.MethodGet, locks+"?path=art/logo.psd", nil)
		require.Equal(t, http.StatusOK, status, "%v", resp)
		assert.Len(t, resp["locks"], 1)
		status, resp = srv.lfsDo(t, f.reporter, http.MethodGet, locks+"?path=other", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, resp["locks"], 0)
		status, _ = srv.lfsDo(t, f.outer, http.MethodGet, locks, nil)
		assert.Equal(t, http.StatusNotFound, status)

		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, locks+"/verify", map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", resp)
		assert.Len(t, resp["ours"], 1)
		assert.Len(t, resp["theirs"], 0)
		_, resp = srv.lfsDo(t, f.maint, http.MethodPost, locks+"/verify", map[string]any{})
		assert.Len(t, resp["ours"], 0)
		assert.Len(t, resp["theirs"], 1)
		status, _ = srv.lfsDo(t, f.reporter, http.MethodPost, locks+"/verify", map[string]any{})
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("pushes", func(t *testing.T) {
		err := tryPushFiles(t, f.url, f.maint.git, "main", map[string]string{"art/logo.psd": "v2\n"})
		require.Error(t, err, "someone else's lock")
		assert.Contains(t, err.Error(), "art/logo.psd is locked by dev")
		err = tryPushFiles(t, f.url, f.maint.git, "main", nil, "art/logo.psd")
		assert.Error(t, err, "deleting it too")
		err = tryPushFiles(t, f.url, f.maint.git, "feature", map[string]string{"art/logo.psd": "v2\n"})
		assert.Error(t, err, "a new branch is compared with the default branch")
		assert.NoError(t, tryPushFiles(t, f.url, f.maint.git, "main", map[string]string{"free.txt": "v2\n"}), "other files")
		assert.NoError(t, tryPushFiles(t, f.url, f.maint.git, "other", map[string]string{"free.txt": "v3\n"}), "a new branch that leaves it alone")
		assert.NoError(t, tryPushFiles(t, f.url, f.dev.git, "main", map[string]string{"art/logo.psd": "v2\n"}), "the lock's owner")
	})

	t.Run("unlock", func(t *testing.T) {
		unlock := locks + "/" + lockId + "/unlock"
		status, resp := srv.lfsDo(t, f.maint, http.MethodPost, unlock, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "not theirs: %v", resp)
		status, resp = srv.lfsDo(t, f.reporter, http.MethodPost, unlock, map[string]any{"force": true})
		assert.Equal(t, http.StatusForbidden, status, "%v", resp)

		// Developers can't force either.
		status, resp = srv.lfsDo(t, f.maint, http.MethodPost, locks, map[string]any{"path": "free.txt"})
		require.Equal(t, http.StatusCreated, status, "%v", resp)
		maintLock := resp["lock"].(map[string]any)["id"].(string)
		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, locks+"/"+maintLock+"/unlock", map[string]any{"force": true})
		assert.Equal(t, http.StatusForbidden, status, "%v", resp)

		status, resp = srv.lfsDo(t, f.maint, http.MethodPost, unlock, map[string]any{"force": true})
		require.Equal(t, http.StatusOK, status, "maintainers force: %v", resp)
		assert.Equal(t, lockId, resp["lock"].(map[string]any)["id"])
		status, _ = srv.lfsDo(t, f.maint, http.MethodPost, unlock, map[string]any{"force": true})
		assert.Equal(t, http.StatusNotFound, status)
		assert.NoError(t, tryPushFiles(t, f.url, f.maint.git, "main", map[string]string{"art/logo.psd": "v3\n"}), "unlocked")

		status, resp = srv.lfsDo(t, f.maint, http.MethodPost, locks+"/"+maintLock+"/unlock", map[string]any{})
		assert.Equal(t, http.StatusOK, status, "their own: %v", resp)
	})
}

func ptr[T any](v T) *T { return &v }
