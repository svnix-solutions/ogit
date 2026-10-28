package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wikiAPI(repo string, rest ...string) string {
	s := repoAPI(repo) + "/wiki"
	for _, r := range rest {
		s += "/" + r
	}
	return s
}

// wikiPage reads a page through the API: status and body.
func (s *testServer) wikiPage(t *testing.T, as user, repo, name string) (int, map[string]any) {
	t.Helper()
	return s.get(t, wikiAPI(repo, "page")+"?name="+url.QueryEscape(name), as.session)
}

// wikiHead is the wiki's tip for edits.
func (s *testServer) wikiHead(t *testing.T, as user, repo string) string {
	t.Helper()
	status, info := s.get(t, wikiAPI(repo), as.session)
	require.Equal(t, http.StatusOK, status, "%v", info)
	head, _ := info["head"].(string)
	return head
}

func (s *testServer) wikiCreate(t *testing.T, as user, repo, name, content string) (int, map[string]any) {
	t.Helper()
	return s.post(t, wikiAPI(repo, "pages"), as.session, map[string]any{"name": name, "content": content})
}

func (s *testServer) wikiEdit(t *testing.T, as user, repo string, body map[string]any) (int, map[string]any) {
	t.Helper()
	return s.request(t, http.MethodPut, wikiAPI(repo, "page"), as.session, body)
}

func content(page map[string]any) string {
	p, _ := page["page"].(map[string]any)
	c, _ := p["content"].(string)
	return c
}

// pushNew pushes files as the first commit of an empty repository.
func pushNew(t *testing.T, repoURL string, auth *githttp.BasicAuth, branch string, files map[string]string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	for name, c := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(c), 0o644))
		_, err := wt.Add(name)
		require.NoError(t, err)
	}
	h, err := wt.Commit("first", &git.CommitOptions{Author: &object.Signature{Name: "Alice", Email: "alice@example.com", When: time.Now()}})
	require.NoError(t, err)
	_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{repoURL}})
	require.NoError(t, err)
	refspec := gitconfig.RefSpec("refs/heads/master:refs/heads/" + branch)
	return h.String(), repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{refspec}})
}

func TestWikis(t *testing.T) {
	t.Run("local", func(t *testing.T) { testWikis(t) })
	t.Run("s3_fake", func(t *testing.T) { testWikis(t, withS3(s3test.StartFake(t))) })
}

func testWikis(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, append(opts, allowLoopback, withPublicURL)...)
	srv := f.srv
	wikiURL := srv.url + "/owner/app.wiki.git"

	t.Run("before_the_first_edit", func(t *testing.T) {
		status, info := srv.get(t, wikiAPI("owner/app"), f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", info)
		assert.Equal(t, "writers", info["access"])
		assert.Equal(t, false, info["exists"])
		assert.Equal(t, false, info["can_edit"], "reporters read")
		assert.Empty(t, info["pages"])
		_, info = srv.get(t, wikiAPI("owner/app"), f.dev.session)
		assert.Equal(t, true, info["can_edit"], "developers edit")
		status, _ = srv.get(t, wikiAPI("owner/app"), f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "a private repository's wiki is hidden")
		_, err := clone(t, wikiURL, f.dev.git)
		assert.Error(t, err, "nothing to clone yet")
		status, _ = srv.wikiPage(t, f.reporter, "owner/app", "Home")
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("pages_in_the_ui", func(t *testing.T) {
		status, body := srv.wikiCreate(t, f.reporter, "owner/app", "Home", "# Hi")
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		status, _ = srv.wikiCreate(t, f.outer, "owner/app", "Home", "# Hi")
		assert.Equal(t, http.StatusNotFound, status)

		status, body = srv.wikiCreate(t, f.dev, "owner/app", "Home", "# Welcome\r\nSee [[Getting Started]].\r\n")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Home", body["name"])
		status, page := srv.wikiPage(t, f.reporter, "owner/app", "")
		require.Equal(t, http.StatusOK, status, "%v", page)
		assert.Equal(t, "# Welcome\nSee [[Getting Started]].\n", content(page), "Home by default, CRLF normalized")
		p := page["page"].(map[string]any)
		assert.Equal(t, "Home.md", p["path"])
		commit := p["commit"].(map[string]any)
		assert.Equal(t, "dev", commit["author"].(map[string]any)["name"])
		assert.Equal(t, "Created Home", commit["message"])
		assert.Equal(t, commit["hash"], page["head"])

		status, _ = srv.wikiCreate(t, f.dev, "owner/app", "Home", "again")
		assert.Equal(t, http.StatusConflict, status, "the page exists")
		for _, bad := range []string{"", "../x", "a/.hidden", "uploads/x", "a:b", "x?", strings.Repeat("a", 201)} {
			status, _ := srv.wikiCreate(t, f.dev, "owner/app", bad, "x")
			assert.Equal(t, http.StatusBadRequest, status, "%q", bad)
		}

		status, body = srv.wikiCreate(t, f.dev, "owner/app", "Docs/Getting Started", "Steps")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Docs/Getting-Started", body["name"], "spaces become dashes")
		_, info := srv.get(t, wikiAPI("owner/app"), f.reporter.session)
		assert.Equal(t, true, info["exists"])
		var names []string
		for _, p := range info["pages"].([]any) {
			names = append(names, p.(map[string]any)["name"].(string))
		}
		assert.Equal(t, []string{"Docs/Getting-Started", "Home"}, names)
		assert.Equal(t, "Getting Started", info["pages"].([]any)[0].(map[string]any)["title"])
	})

	t.Run("conflicts", func(t *testing.T) {
		base := srv.wikiHead(t, f.dev, "owner/app")
		status, body := srv.wikiEdit(t, f.maint, "owner/app", map[string]any{"name": "Home", "content": "maint's", "base": base})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Home", "content": "dev's", "base": base})
		assert.Equal(t, http.StatusConflict, status, "Home changed since: %v", body)
		status, body = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Docs/Getting-Started", "content": "Steps, edited", "base": base})
		require.Equal(t, http.StatusOK, status, "another page changed meanwhile: no conflict: %v", body)
		_, page := srv.wikiPage(t, f.reporter, "owner/app", "Home")
		assert.Equal(t, "maint's", content(page), "the other edit is kept")

		head := srv.wikiHead(t, f.dev, "owner/app")
		status, body = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Home", "content": "maint's", "base": head})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, head, body["commit"], "unchanged: no commit")
		status, _ = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Nope", "content": "x", "base": head})
		assert.Equal(t, http.StatusConflict, status, "a page gone since it was read")
		status, _ = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Nope", "content": "x"})
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Home", "content": "x", "base": strings.Repeat("0", 40)})
		assert.Equal(t, http.StatusConflict, status, "an unknown base")
	})

	t.Run("rename_and_delete", func(t *testing.T) {
		srv.wikiCreate(t, f.dev, "owner/app", "Old Name", "moving")
		head := srv.wikiHead(t, f.dev, "owner/app")
		status, body := srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Old-Name", "new_name": "Home", "content": "x", "base": head})
		assert.Equal(t, http.StatusConflict, status, "onto an existing page: %v", body)
		status, body = srv.wikiEdit(t, f.dev, "owner/app", map[string]any{"name": "Old-Name", "new_name": "New Name", "content": "moved", "base": head})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "New-Name", body["name"])
		status, _ = srv.wikiPage(t, f.dev, "owner/app", "Old-Name")
		assert.Equal(t, http.StatusNotFound, status)
		_, page := srv.wikiPage(t, f.dev, "owner/app", "New-Name")
		assert.Equal(t, "moved", content(page))
		assert.Equal(t, "Renamed Old Name to New Name", page["page"].(map[string]any)["commit"].(map[string]any)["message"])

		status, body = srv.request(t, http.MethodDelete, wikiAPI("owner/app", "page")+"?name=New-Name&base="+srv.wikiHead(t, f.dev, "owner/app"), f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		status, body = srv.request(t, http.MethodDelete, wikiAPI("owner/app", "page")+"?name=New-Name&base="+srv.wikiHead(t, f.dev, "owner/app"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.wikiPage(t, f.dev, "owner/app", "New-Name")
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("sidebar_and_footer", func(t *testing.T) {
		srv.wikiCreate(t, f.dev, "owner/app", "_Sidebar", "root sidebar")
		srv.wikiCreate(t, f.dev, "owner/app", "Docs/_Footer", "docs footer")
		_, page := srv.wikiPage(t, f.reporter, "owner/app", "Docs/Getting-Started")
		require.NotNil(t, page["sidebar"], "%v", page)
		require.NotNil(t, page["footer"], "the folder's own: %v", page)
		assert.Equal(t, "root sidebar", page["sidebar"].(map[string]any)["content"], "the nearest one, above")
		assert.Equal(t, "docs footer", page["footer"].(map[string]any)["content"])
		_, page = srv.wikiPage(t, f.reporter, "owner/app", "Home")
		assert.NotNil(t, page["sidebar"])
		assert.Nil(t, page["footer"], "the Docs footer is for Docs")
		_, info := srv.get(t, wikiAPI("owner/app"), f.reporter.session)
		assert.Len(t, info["pages"], 4, "special pages are pages too")
	})

	t.Run("history_diff_and_revert", func(t *testing.T) {
		wiki := url.PathEscape("owner/app.wiki")
		status, log := srv.get(t, "/api/v1/repository/"+wiki+"/commits?spec=main/Home.md", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", log)
		commits := log["commits"].([]any)
		require.Len(t, commits, 2, "Home's history: created, edited")
		first := commits[1].(map[string]any)["hash"].(string)
		latest := commits[0].(map[string]any)["hash"].(string)
		status, diff := srv.get(t, "/api/v1/repository/"+wiki+"/commits/"+latest, f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", diff)
		assert.Equal(t, "Home.md", diff["diff"].(map[string]any)["files"].([]any)[0].(map[string]any)["path"])
		status, _ = srv.get(t, "/api/v1/repository/"+wiki+"/commits?spec=main/Home.md", f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)

		_, old := srv.get(t, wikiAPI("owner/app", "page")+"?name=Home&version="+first, f.reporter.session)
		assert.Equal(t, "# Welcome\nSee [[Getting Started]].\n", content(old), "a page at a version")

		status, body := srv.post(t, wikiAPI("owner/app", "revert"), f.reporter.session, map[string]any{"name": "Home", "version": first})
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		stale := srv.wikiHead(t, f.dev, "owner/app")
		status, body = srv.wikiEdit(t, f.maint, "owner/app", map[string]any{"name": "Home", "content": "maint again", "base": stale})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, wikiAPI("owner/app", "revert"), f.dev.session, map[string]any{"name": "Home", "version": first, "base": stale})
		assert.Equal(t, http.StatusConflict, status, "Home changed since: %v", body)
		status, body = srv.post(t, wikiAPI("owner/app", "revert"), f.dev.session, map[string]any{"name": "Home", "version": first, "base": srv.wikiHead(t, f.dev, "owner/app")})
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, page := srv.wikiPage(t, f.dev, "owner/app", "Home")
		assert.Equal(t, content(old), content(page))
		assert.Equal(t, "Reverted Home to "+first[:7], page["page"].(map[string]any)["commit"].(map[string]any)["message"])
	})

	t.Run("search", func(t *testing.T) {
		status, body := srv.get(t, wikiAPI("owner/app", "search")+"?q=welcome+started", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		results := body["results"].([]any)
		require.Len(t, results, 1, "every word: %v", results)
		assert.Equal(t, "Home", results[0].(map[string]any)["name"])
		assert.Contains(t, results[0].(map[string]any)["snippet"], "Welcome")
		_, body = srv.get(t, wikiAPI("owner/app", "search")+"?q=STEPS", f.reporter.session)
		assert.Len(t, body["results"], 1, "case-insensitive")
		_, body = srv.get(t, wikiAPI("owner/app", "search")+"?q=getting", f.reporter.session)
		assert.Len(t, body["results"], 2, "titles count")
		status, _ = srv.get(t, wikiAPI("owner/app", "search")+"?q=steps", f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("over_git", func(t *testing.T) {
		_, err := clone(t, wikiURL, f.outer.git)
		assert.Error(t, err, "outsiders")
		_, err = clone(t, wikiURL, nil)
		assert.Error(t, err, "anonymous callers on a private repository")
		repo, err := clone(t, wikiURL, f.reporter.git)
		require.NoError(t, err, "readers clone")
		head, err := repo.Head()
		require.NoError(t, err)
		assert.Equal(t, "refs/heads/main", head.Name().String())

		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "main", "require_pull_request": true})
		require.Error(t, tryPushFiles(t, f.url, f.dev.git, "main", map[string]string{"p.txt": "p"}), "main is protected")
		pushFiles(t, wikiURL, f.dev.git, "main", "from git", map[string]string{"Pushed.md": "over git", "notes.txt": "not a page"})
		_, page := srv.wikiPage(t, f.reporter, "owner/app", "Pushed")
		assert.Equal(t, "over git", content(page))
		err = tryPushFiles(t, wikiURL, f.reporter.git, "main", map[string]string{"R.md": "r"})
		assert.Error(t, err, "reporters don't push")
		assert.NotContains(t, srv.refsOf(t, "owner/app"), "refs/heads/wiki", "the repository is untouched")
	})

	t.Run("readers_edit_when_allowed", func(t *testing.T) {
		mode := func(as user, m string) int {
			status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), as.session, map[string]any{"wiki_access": m})
			return status
		}
		assert.Equal(t, http.StatusForbidden, mode(f.dev, "readers"), "maintainers decide")
		assert.Equal(t, http.StatusBadRequest, mode(f.maint, "everyone"))
		require.Equal(t, http.StatusOK, mode(f.maint, "readers"))
		_, repo := srv.get(t, repoAPI("owner/app"), f.reporter.session)
		assert.Equal(t, "readers", repo["wiki_access"])
		status, body := srv.wikiCreate(t, f.reporter, "owner/app", "By Reporter", "hello")
		require.Equal(t, http.StatusOK, status, "%v", body)
		require.NoError(t, tryPushFiles(t, wikiURL, f.reporter.git, "main", map[string]string{"R.md": "r"}), "and push")
		data := randomBytes(t, 200)
		status, resp := srv.lfsDo(t, f.reporter, http.MethodPost, lfsPath("owner/app.wiki", "objects/batch"), batch("upload", lfsObject(data)))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, header := action(t, resp, "upload")
		status, got := srv.lfsSend(t, http.MethodPut, strings.TrimPrefix(href, "https://git.example.com"), header, data)
		assert.Equal(t, http.StatusOK, status, "and upload LFS objects: %s", got)
		status, _ = srv.wikiCreate(t, f.outer, "owner/app", "By Outer", "hello")
		assert.Equal(t, http.StatusNotFound, status, "readers only")

		require.Equal(t, http.StatusOK, mode(f.maint, "off"))
		status, _ = srv.get(t, wikiAPI("owner/app"), f.owner.session)
		assert.Equal(t, http.StatusNotFound, status, "off: hidden")
		status, _ = srv.wikiPage(t, f.owner, "owner/app", "Home")
		assert.Equal(t, http.StatusNotFound, status)
		_, err := clone(t, wikiURL, f.owner.git)
		assert.Error(t, err, "git too")
		status, _ = srv.get(t, "/api/v1/repository/"+url.PathEscape("owner/app.wiki")+"/commits?spec=main", f.owner.session)
		assert.Equal(t, http.StatusNotFound, status, "and browsing")

		require.Equal(t, http.StatusOK, mode(f.maint, "writers"))
		_, page := srv.wikiPage(t, f.owner, "owner/app", "By-Reporter")
		assert.Equal(t, "hello", content(page), "turning it on again keeps the content")
		status, _ = srv.wikiCreate(t, f.reporter, "owner/app", "Again", "no")
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("attachments", func(t *testing.T) {
		upload := func(as user, name string, data []byte) (int, map[string]any) {
			req, err := http.NewRequest(http.MethodPost, srv.url+wikiAPI("owner/app", "uploads")+"?name="+url.QueryEscape(name), bytes.NewReader(data))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+as.session)
			req.Header.Set("Content-Type", "application/octet-stream")
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)
			return resp.StatusCode, out
		}
		status, _ := upload(f.reporter, "x.png", []byte("x"))
		assert.Equal(t, http.StatusForbidden, status)
		png := append([]byte("\x89PNG\r\n\x1a\n"), randomBytes(t, 2000)...)
		status, body := upload(f.dev, "My Diagram.png", png)
		require.Equal(t, http.StatusOK, status, "%v", body)
		path := body["path"].(string)
		assert.Regexp(t, `^uploads/[0-9a-f]{12}/My-Diagram\.png$`, path)

		status, hdr, got := fetch(t, srv.url+"/owner/app.wiki/raw/main/"+path, basic(f.reporter.git))
		require.Equal(t, http.StatusOK, status, "%s", got)
		assert.Equal(t, png, got, "the LFS object, not the pointer")
		assert.Equal(t, "nosniff", hdr.Get("X-Content-Type-Options"))
		status, _, _ = fetch(t, srv.url+"/owner/app.wiki/raw/main/"+path, basic(f.outer.git))
		assert.Equal(t, http.StatusNotFound, status)

		status, body = upload(f.dev, "evil.html", []byte("<script>alert(1)</script>"))
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, hdr, _ = fetch(t, srv.url+"/owner/app.wiki/raw/main/"+body["path"].(string), basic(f.dev.git))
		assert.NotContains(t, hdr.Get("Content-Type"), "html", "never HTML on our origin")
		assert.Contains(t, hdr.Get("Content-Security-Policy"), "sandbox")

		status, _ = upload(f.dev, "", []byte("x"))
		assert.Equal(t, http.StatusBadRequest, status)
		srv.deps.WikiApp.MaxAttachment = 100
		status, _ = upload(f.dev, "big.bin", randomBytes(t, 101))
		assert.Equal(t, http.StatusBadRequest, status, "over the limit")
		srv.deps.WikiApp.MaxAttachment = 0

		_, info := srv.get(t, wikiAPI("owner/app"), f.dev.session)
		for _, p := range info["pages"].([]any) {
			assert.NotContains(t, p.(map[string]any)["path"], "uploads/", "attachments aren't pages")
		}
		_, page := srv.get(t, "/api/v1/repository/"+url.PathEscape("owner/app.wiki")+"/blob?spec=main/.gitattributes", f.dev.session)
		assert.Contains(t, page["file"].(map[string]any)["content"], "uploads/** filter=lfs", "clones check attachments out as files")
	})

	t.Run("lfs_tokens_are_bound_to_the_wiki", func(t *testing.T) {
		data := randomBytes(t, 300)
		obj := lfsObject(data)
		status, resp := srv.lfsDo(t, f.dev, http.MethodPost, lfsPath("owner/app.wiki", "objects/batch"), batch("upload", obj))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		href, header := action(t, resp, "upload")
		wikiObject := lfsPath("owner/app.wiki", "objects/"+obj["oid"].(string))
		assert.Equal(t, "https://git.example.com"+wikiObject, href)
		status, body := srv.lfsSend(t, http.MethodPut, lfsPath("owner/app", "objects/"+obj["oid"].(string)), header, data)
		assert.Equal(t, http.StatusNotFound, status, "the wiki's token isn't the repository's: %s", body)
		status, body = srv.lfsSend(t, http.MethodPut, wikiObject, header, data)
		require.Equal(t, http.StatusOK, status, "%s", body)

		status, resp = srv.lfsDo(t, f.dev, http.MethodPost, lfsPath("owner/app", "objects/batch"), batch("upload", obj))
		require.Equal(t, http.StatusOK, status, "%v", resp)
		_, header = action(t, resp, "upload")
		status, body = srv.lfsSend(t, http.MethodPut, wikiObject, header, data)
		assert.Equal(t, http.StatusNotFound, status, "nor the other way: %s", body)
	})

	t.Run("archived", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": true})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.wikiCreate(t, f.dev, "owner/app", "Late", "x")
		assert.Equal(t, http.StatusConflict, status)
		err := tryPushFiles(t, wikiURL, f.owner.git, "main", map[string]string{"L.md": "l"})
		assert.Error(t, err)
		_, err = clone(t, wikiURL, f.reporter.git)
		assert.NoError(t, err, "reads keep working")
		status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": false})
		require.Equal(t, http.StatusOK, status)
	})

	var logo string
	t.Run("public_repositories", func(t *testing.T) {
		srv.createRepo(t, f.owner.session, "Open", false)
		_, err := pushNew(t, srv.url+"/owner/open.wiki.git", f.owner.git, "master", map[string]string{"Home.md": "public"})
		require.NoError(t, err, "the first push creates the wiki")
		status, page := srv.get(t, wikiAPI("owner/open", "page"), "")
		require.Equal(t, http.StatusOK, status, "anonymous: %v", page)
		assert.Equal(t, "public", content(page), "HEAD follows the first pushed branch (master)")
		cloned, err := clone(t, srv.url+"/owner/open.wiki.git", nil)
		require.NoError(t, err, "anonymous clone")
		head, err := cloned.Head()
		require.NoError(t, err)
		tip, err := cloned.CommitObject(head.Hash())
		require.NoError(t, err)
		_, err = tip.File("Home.md")
		assert.NoError(t, err, "the wiki, not the repository")
		req, err := http.NewRequest(http.MethodPost, srv.url+wikiAPI("owner/open", "uploads")+"?name=logo.png", strings.NewReader("logo bytes"))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+f.owner.session)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var att map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&att))
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "%v", att)
		logo = att["path"].(string)
		err = tryPushFiles(t, srv.url+"/owner/open.wiki.git", f.outer.git, "other", map[string]string{"X.md": "x"})
		assert.Error(t, err, "outsiders read a public wiki, never edit it")
		status, _ = srv.wikiCreate(t, f.outer, "owner/open", "X", "x")
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("rename_fork_delete", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPatch, repoAPI("owner/open"), f.owner.session, map[string]any{"name": "Opened"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, page := srv.wikiPage(t, f.owner, "owner/opened", "Home")
		assert.Equal(t, "public", content(page), "the wiki follows renames")

		fork := srv.fork(t, f.outer, "owner/opened", nil)
		_, page = srv.wikiPage(t, f.outer, fork["full_path"].(string), "Home")
		assert.Equal(t, "public", content(page), "forks copy the wiki")

		status, body = srv.post(t, wikiAPI(fork["full_path"].(string), "pages"), f.outer.session, map[string]any{"name": "Mine", "content": "fork only"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.wikiPage(t, f.owner, "owner/opened", "Mine")
		assert.Equal(t, http.StatusNotFound, status, "a copy, not shared")
		status, _, got := fetch(t, srv.url+"/"+fork["full_path"].(string)+".wiki/raw/master/"+logo, nil)
		require.Equal(t, http.StatusOK, status, "%s", got)
		assert.Equal(t, "logo bytes", string(got), "the fork reads the original's attachments")

		status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/opened"), f.owner.session, map[string]any{"wiki_access": "off"})
		require.Equal(t, http.StatusOK, status)
		hidden := srv.fork(t, f.dev, "owner/opened", nil)
		_, info := srv.get(t, wikiAPI(hidden["full_path"].(string)), f.dev.session)
		assert.Equal(t, false, info["exists"], "a wiki that is off isn't copied")

		_, repo := srv.get(t, repoAPI("owner/opened"), f.owner.session)
		key := srv.storageKey(t, repo["id"].(string)) + domain.WikiSuffix
		require.True(t, srv.deps.Storage.RepositoryExists(key))
		status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/opened")+"?confirm=opened", f.owner.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.False(t, srv.deps.Storage.RepositoryExists(key), "deleted with the repository")
	})
}

// TestWikiConcurrentEdits: edits of different pages at the same time all
// land (the branch moves by compare-and-swap, retried on a moved tip).
func TestWikiConcurrentEdits(t *testing.T) {
	t.Run("local", func(t *testing.T) { testWikiConcurrentEdits(t) })
	t.Run("s3_fake", func(t *testing.T) { testWikiConcurrentEdits(t, withS3(s3test.StartFake(t))) })
}

func testWikiConcurrentEdits(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", true)
	status, body := srv.wikiCreate(t, owner, "owner/app", "Home", "start")
	require.Equal(t, http.StatusOK, status, "%v", body)
	const n = 3
	var wg sync.WaitGroup
	statuses := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			statuses[i], _ = srv.wikiCreate(t, owner, "owner/app", fmt.Sprintf("Page %d", i), "x")
		}()
	}
	wg.Wait()
	for i, st := range statuses {
		assert.Equal(t, http.StatusOK, st, "page %d", i)
	}
	_, info := srv.get(t, wikiAPI("owner/app"), owner.session)
	assert.Len(t, info["pages"], n+1, "none lost")
}

func (s *testServer) storageKey(t *testing.T, id string) string {
	t.Helper()
	repo, err := s.deps.RepoApp.GetRepository(&id, nil)
	require.NoError(t, err)
	return repo.StorageKey
}

func TestWikiEvents(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback, withPublicURL)
	srv := f.srv
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"gollum", "push"}})
	srv.deliver(t)
	rcv.take()
	collect := func() []hookEvent { srv.deliver(t); return rcv.takeEvents() }
	srv.watch(t, f.reporter, "owner/app", "all")

	status, body := srv.wikiCreate(t, f.dev, "owner/app", "Home", "hi")
	require.Equal(t, http.StatusOK, status, "%v", body)
	evs := collect()
	require.Equal(t, []string{"gollum."}, names(evs), "a wiki edit is no push")
	pages := evs[0].payload["pages"].([]any)
	require.Len(t, pages, 1)
	p := pages[0].(map[string]any)
	assert.Equal(t, "Home", p["page_name"])
	assert.Equal(t, "created", p["action"])
	assert.Equal(t, body["commit"], p["sha"])
	assert.Equal(t, "https://git.example.com/owner/app/wiki/Home", p["html_url"])
	assert.Equal(t, "dev", evs[0].payload["sender"].(map[string]any)["login"])
	assert.Equal(t, "owner/app", evs[0].payload["repository"].(map[string]any)["full_name"])

	n := srv.inbox(t, f.reporter, "")["Wiki"]
	require.NotNil(t, n, "watchers at all are notified")
	assert.Equal(t, "dev created the wiki page Home", n["summary"])
	assert.Equal(t, "/owner/app/wiki/Home", n["subject"].(map[string]any)["url"])
	assert.Nil(t, srv.inbox(t, f.maint, "")["Wiki"], "participating watchers aren't")
	assert.Nil(t, srv.inbox(t, f.dev, "")["Wiki"], "nor the author")

	pushFiles(t, srv.url+"/owner/app.wiki.git", f.maint.git, "main", "two pages", map[string]string{"Home.md": "edited", "New.md": "new", "x.txt": "x"})
	evs = collect()
	require.Equal(t, []string{"gollum."}, names(evs), "a wiki push: gollum only")
	got := map[string]string{}
	for _, p := range evs[0].payload["pages"].([]any) {
		got[p.(map[string]any)["page_name"].(string)] = p.(map[string]any)["action"].(string)
	}
	assert.Equal(t, map[string]string{"Home": "edited", "New": "created"}, got, "pages only")
	assert.Equal(t, "maint edited 2 wiki pages", srv.inbox(t, f.reporter, "")["Wiki"]["summary"])

	pushFiles(t, srv.url+"/owner/app.wiki.git", f.maint.git, "draft", "elsewhere", map[string]string{"D.md": "d"})
	assert.Empty(t, collect(), "only the default branch")
	pushFiles(t, f.url, f.dev.git, "main", "code", map[string]string{"c.txt": "c"})
	assert.Equal(t, []string{"push."}, names(collect()), "the repository's pushes are push events")
}

// TestWikiOverSSH uses the real git client: <path>.wiki.git over SSH.
func TestWikiOverSSH(t *testing.T) {
	srv := startTestServer(t)
	owner, reporter := srv.newUser(t, "owner"), srv.newUser(t, "reporter")
	srv.createRepo(t, owner.session, "App", true)
	status, _ := srv.post(t, repoAPI("owner/app")+"/members", owner.session, map[string]any{"username": "reporter", "role": "reporter"})
	require.Equal(t, http.StatusOK, status)
	port := srv.startSSH(t)
	keys := map[string]*gitCLI{}
	for _, u := range []user{owner, reporter} {
		key := newSSHKey(t, u.name+"@laptop")
		status, body := srv.post(t, "/api/v1/user/ssh-keys", u.session, map[string]any{"key": key.authorized})
		require.Equal(t, http.StatusOK, status, "%v", body)
		keys[u.name] = newGitCLI(t, key, port)
	}
	g := keys["owner"]
	dir := t.TempDir()
	g.mustRun(dir, "init", "-b", "main")
	commitFile(t, g, dir, "Home.md", "over ssh\n")
	g.mustRun(dir, "remote", "add", "origin", g.url("owner/app.wiki"))
	g.mustRun(dir, "push", "origin", "main")
	_, page := srv.wikiPage(t, owner, "owner/app", "Home")
	assert.Equal(t, "over ssh\n", content(page))

	r := keys["reporter"]
	rdir := t.TempDir()
	r.mustRun(rdir, "clone", r.url("owner/app.wiki"), ".")
	commitFile(t, r, rdir, "Home.md", "reporter\n")
	out, err := r.run(rdir, "push", "origin", "main")
	assert.Error(t, err, "reporters read only: %s", out)
	assert.Contains(t, out, "denied")

	_, repo := srv.get(t, repoAPI("owner/app"), owner.session)
	status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), owner.session, map[string]any{"wiki_access": "off"})
	require.Equal(t, http.StatusOK, status)
	out, err = g.run(t.TempDir(), "clone", g.url("owner/app.wiki"), ".")
	assert.Error(t, err, "off: %s", out)
	assert.Contains(t, out, "not found")
	status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), owner.session, map[string]any{"wiki_access": "writers"})
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, srv.deps.RepoApp.RepoPres.Update(repo["id"].(string), map[string]any{"is_mirror": true}))
	commitFile(t, g, dir, "Home.md", "on a mirror\n")
	out, err = g.run(dir, "push", "origin", "main")
	assert.Error(t, err, "%s", out)
	assert.Contains(t, out, "mirror", "a mirror's wiki is read-only over SSH too")
}

func TestWikiMirrors(t *testing.T) {
	srv := startTestServer(t, allowLoopbackMirrors)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Source", true)
	pushFiles(t, srv.url+"/alice/source.git", alice.git, "main", "code", map[string]string{"a.txt": "a"})
	srv.wikiCreate(t, alice, "alice/source", "Home", "from the source")
	read := srv.createToken(t, alice.session, "mirror-read", "read:repository")

	t.Run("pull_mirrors_and_imports_fetch_it", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Mirror", "url": srv.url + "/alice/source.git",
			"username": read.Username, "password": read.Password, "mirror": true, "interval_seconds": 3600})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.syncMirrors(t)
		_, page := srv.wikiPage(t, bob, "bob/mirror", "Home")
		assert.Equal(t, "from the source", content(page))
		status, _ = srv.wikiCreate(t, bob, "bob/mirror", "Mine", "x")
		assert.Equal(t, http.StatusConflict, status, "a mirror's wiki is read-only")

		// A repository without a wiki: none made.
		srv.createRepo(t, alice.session, "Bare", true)
		pushFiles(t, srv.url+"/alice/bare.git", alice.git, "main", "code", map[string]string{"a.txt": "a"})
		status, body = srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": srv.url + "/alice/bare.git",
			"username": read.Username, "password": read.Password})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.syncMirrors(t)
		_, info := srv.get(t, wikiAPI("bob/copy"), bob.session)
		assert.Equal(t, false, info["exists"])
	})

	t.Run("push_mirrors_push_it", func(t *testing.T) {
		srv.createRepo(t, bob.session, "Target", true)
		_, err := pushNew(t, srv.url+"/bob/target.wiki.git", bob.git, "main", map[string]string{"Home.md": "old"})
		require.NoError(t, err)
		write := srv.createToken(t, bob.session, "mirror-write", "read:repository", "write:repository")
		status, body := srv.post(t, repoAPI("alice/source")+"/push-mirrors", alice.session, map[string]any{"url": srv.url + "/bob/target.git",
			"username": write.Username, "password": write.Password})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.syncPushMirrors(t)
		_, page := srv.wikiPage(t, bob, "bob/target", "Home")
		assert.Equal(t, "from the source", content(page))

		srv.wikiEdit(t, alice, "alice/source", map[string]any{"name": "Home", "content": "edited"})
		srv.syncPushMirrors(t)
		_, page = srv.wikiPage(t, bob, "bob/target", "Home")
		assert.Equal(t, "edited", content(page), "a wiki edit queues the push mirrors")
	})
}
