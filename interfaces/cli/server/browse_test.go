package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	cfg "github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pushFiles clones repoURL, writes files on branch (created from main if
// new), commits and pushes. It returns the commit hash.
func pushFiles(t *testing.T, repoURL string, auth *githttp.BasicAuth, branch, msg string, files map[string]string) string {
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
	h, err := wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "Alice", Email: "alice@example.com", When: time.Now()}})
	require.NoError(t, err)
	refspec := config.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)
	require.NoError(t, repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []config.RefSpec{refspec}}))
	return h.String()
}

// fetch GETs a non-API URL with optional headers.
func fetch(t *testing.T, url string, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, body
}

func basic(a *githttp.BasicAuth) map[string]string {
	return map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(a.Username+":"+a.Password))}
}

func TestBrowseRepositories(t *testing.T) {
	t.Run("local", func(t *testing.T) { testBrowseRepositories(t) })
	t.Run("s3_fake", func(t *testing.T) { testBrowseRepositories(t, withS3(s3test.StartFake(t))) })
}

func testBrowseRepositories(t *testing.T, opts ...func(*cfg.Config)) {
	srv := startTestServer(t, opts...)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Pub", false)
	srv.createRepo(t, alice.session, "Priv", true)
	pub := srv.url + "/alice/pub.git"
	first := pushFiles(t, pub, alice.git, "main", "add docs", map[string]string{
		"README.md":     "# Pub\n\nHello\n",
		"docs/guide.md": "line one\nline two\n",
		"page.html":     "<script>alert(1)</script>",
		"logo.png":      "\x89PNG\r\n\x1a\n\x00\x00",
		"data.bin":      "a\x00b",
	})
	feature := pushFiles(t, pub, alice.git, "feature/x", "feature work", map[string]string{"docs/guide.md": "line one\nline 2\n"})
	pushFiles(t, srv.url+"/alice/priv.git", alice.git, "main", "secret", map[string]string{"secret.txt": "s3cr3t\n"})
	api := func(path string) string { return repoAPI("alice/pub") + path }

	t.Run("anonymous_reads_public", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("alice/pub"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "none", body["role"])

		status, body = srv.get(t, api("/refs"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "main", body["default_branch"])
		assert.Len(t, body["branches"], 2)

		status, body = srv.get(t, api("/tree"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "main", body["ref"])
		assert.Equal(t, "# Pub\n\nHello\n", body["readme"].(map[string]any)["content"])
		names := []string{}
		for _, e := range body["entries"].([]any) {
			names = append(names, e.(map[string]any)["name"].(string))
		}
		assert.Equal(t, []string{"docs", "data.bin", "logo.png", "page.html", "README.md"}, names)

		status, body = srv.get(t, api("/blob?spec=feature/x/docs/guide.md"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "feature/x", body["ref"], "a branch with a slash")
		assert.Equal(t, "line one\nline 2\n", body["file"].(map[string]any)["content"])

		status, body = srv.get(t, api("/blob?spec=main/data.bin"), "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, body["file"].(map[string]any)["binary"])

		status, body = srv.get(t, api("/commits?spec=main/docs"), "")
		require.Equal(t, http.StatusOK, status)
		require.Len(t, body["commits"], 1, "only the commit touching docs")
		assert.Equal(t, first, body["commits"].([]any)[0].(map[string]any)["hash"])

		status, body = srv.get(t, api("/commits/"+feature), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		files := body["diff"].(map[string]any)["files"].([]any)
		require.Len(t, files, 1)
		assert.Equal(t, "docs/guide.md", files[0].(map[string]any)["path"])

		status, body = srv.get(t, api("/compare?base=main&head=feature/x"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Len(t, body["commits"], 1)

		status, body = srv.get(t, api("/blame?spec=main/docs/guide.md"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Len(t, body["lines"], 2)

		for path, typ := range map[string]string{
			"/tree?spec=nope":           "REF_NOT_FOUND",
			"/blob?spec=main/missing":   "PATH_NOT_FOUND",
			"/commits/" + first[:7]:     "REF_NOT_FOUND",
			"/blame?spec=main/data.bin": "TOO_LARGE",
		} {
			status, body := srv.get(t, api(path), "")
			assert.NotEqual(t, http.StatusOK, status, path)
			assert.Equal(t, typ, body["type"], path)
		}
	})

	t.Run("private_needs_access", func(t *testing.T) {
		priv := repoAPI("alice/priv")
		for _, path := range []string{"", "/refs", "/tree", "/blob?spec=main/secret.txt", "/commits", "/blame?spec=main/secret.txt"} {
			status, _ := srv.get(t, priv+path, "")
			assert.Equal(t, http.StatusNotFound, status, "anonymous %s", path)
			status, _ = srv.get(t, priv+path, bob.session)
			assert.Equal(t, http.StatusNotFound, status, "outsider %s", path)
		}
		status, body := srv.get(t, priv+"/blob?spec=main/secret.txt", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "s3cr3t\n", body["file"].(map[string]any)["content"])

		b := newBrowser(t, srv)
		b.login("alice@example.com")
		status, _, _ = b.do(http.MethodGet, priv+"/tree", nil, false)
		assert.Equal(t, http.StatusOK, status, "the web cookie works too")
	})

	t.Run("raw_files", func(t *testing.T) {
		status, h, body := fetch(t, srv.url+"/alice/pub/raw/main/docs/guide.md", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "line one\nline two\n", string(body))
		assert.Equal(t, "text/plain; charset=utf-8", h.Get("Content-Type"))
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
		assert.Contains(t, h.Get("Content-Security-Policy"), "sandbox")

		_, h, _ = fetch(t, srv.url+"/alice/pub/raw/main/page.html", nil)
		assert.Equal(t, "text/plain; charset=utf-8", h.Get("Content-Type"), "HTML is never served as HTML")
		_, h, _ = fetch(t, srv.url+"/alice/pub/raw/main/logo.png", nil)
		assert.Equal(t, "image/png", h.Get("Content-Type"))
		_, h, body = fetch(t, srv.url+"/alice/pub/raw/main/data.bin", nil)
		assert.Equal(t, "application/octet-stream", h.Get("Content-Type"))
		assert.Contains(t, h.Get("Content-Disposition"), "attachment")
		assert.Equal(t, "a\x00b", string(body))
		status, _, body = fetch(t, srv.url+"/alice/pub/raw/feature/x/docs/guide.md", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "line one\nline 2\n", string(body))
		status, _, _ = fetch(t, srv.url+"/alice/pub/raw/main/docs", nil)
		assert.Equal(t, http.StatusNotFound, status, "not a file")

		secret := srv.url + "/alice/priv/raw/main/secret.txt"
		status, _, _ = fetch(t, secret, nil)
		assert.Equal(t, http.StatusNotFound, status, "anonymous")
		status, _, _ = fetch(t, secret, basic(bob.git))
		assert.Equal(t, http.StatusNotFound, status, "an outsider's token")
		status, _, body = fetch(t, secret, basic(alice.git))
		require.Equal(t, http.StatusOK, status, "the owner's token")
		assert.Equal(t, "s3cr3t\n", string(body))
		status, h, _ = fetch(t, secret, basic(&githttp.BasicAuth{Username: "alice-git", Password: "ogit_wrong"}))
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.NotEmpty(t, h.Get("WWW-Authenticate"))

		b := newBrowser(t, srv)
		b.login("alice@example.com")
		resp, err := b.http.Get(secret)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the web cookie")
	})

	t.Run("archives", func(t *testing.T) {
		status, h, body := fetch(t, srv.url+"/alice/pub/archive/feature/x.zip", nil)
		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, h.Get("Content-Disposition"), `filename=pub-feature-x.zip`)
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		require.NoError(t, err)
		names := map[string]bool{}
		for _, f := range zr.File {
			names[f.Name] = true
		}
		assert.True(t, names["pub-feature-x/docs/guide.md"])

		status, _, body = fetch(t, srv.url+"/alice/pub/archive/main.tar.gz", nil)
		require.Equal(t, http.StatusOK, status)
		gz, err := gzip.NewReader(bytes.NewReader(body))
		require.NoError(t, err)
		tr := tar.NewReader(gz)
		found := false
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			found = found || hdr.Name == "pub-main/README.md"
		}
		assert.True(t, found)

		status, _, _ = fetch(t, srv.url+"/alice/pub/archive/main.rar", nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = fetch(t, srv.url+"/alice/priv/archive/main.zip", nil)
		assert.Equal(t, http.StatusNotFound, status)
	})
}
