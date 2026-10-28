package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/forge"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeForge serves one story through GitHub's, GitLab's or Gitea's API:
// labels bug and feature; milestones v1 (closed) and v2; issue A (closed,
// by alice, two comments, one by a stranger); merged pull request B (alice,
// from branch feature, approved by alice with an inline comment); issue C
// (open, by a stranger, assigned to alice and the stranger); open pull
// request D from a fork (approved by the stranger, changes requested by
// alice); release v1.0.0 with app.bin and huge.iso (too large).
type fakeForge struct {
	kind                 string
	token                string
	main, feature, patch string // commits
	srv                  *httptest.Server
	rateLimit, failPulls atomic.Bool
	calls                atomic.Int64
	asset                []byte
	rateLimitedUntil     time.Time
}

const forgeToken = "forge-token"

func startFakeForge(t *testing.T, kind, main, feature, patch string) *fakeForge {
	f := &fakeForge{kind: kind, token: forgeToken, main: main, feature: feature, patch: patch, asset: []byte("binary-content")}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForge) authorized(r *http.Request) bool {
	switch f.kind {
	case forge.GitLab:
		return r.Header.Get("PRIVATE-TOKEN") == f.token
	case forge.Gitea:
		return r.Header.Get("Authorization") == "token "+f.token
	}
	return r.Header.Get("Authorization") == "Bearer "+f.token
}

func at(day int) string { return time.Date(2024, 3, day, 10, 0, 0, 0, time.UTC).Format(time.RFC3339) }

func (f *fakeForge) serve(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	if !f.authorized(r) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	if f.rateLimit.Load() {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(f.rateLimitedUntil.Unix(), 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return
	}
	path := r.URL.EscapedPath()
	var body any
	switch f.kind {
	case forge.GitLab:
		body = f.gitlab(w, r, strings.TrimPrefix(path, "/projects/alice%2Fsource"))
	default:
		body = f.githubLike(w, r, strings.TrimPrefix(path, "/repos/alice/source"))
	}
	if body == nil {
		http.NotFound(w, r)
		return
	}
	if b, ok := body.([]byte); ok {
		_, _ = w.Write(b)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func forgeUser(login string) map[string]any { return map[string]any{"login": login, "username": login} }

func (f *fakeForge) githubLike(w http.ResponseWriter, r *http.Request, path string) any {
	q := r.URL.Query()
	gitea := f.kind == forge.Gitea
	label := func(names ...string) []any {
		var out []any
		for _, n := range names {
			out = append(out, map[string]any{"name": n})
		}
		return out
	}
	issueA := map[string]any{"number": 1, "title": "Crash on start", "body": "It crashes.", "user": forgeUser("alice"), "state": "closed",
		"created_at": at(1), "updated_at": at(2), "closed_at": at(2), "labels": label("bug"), "milestone": map[string]any{"title": "v1"}, "comments": 2}
	issueC := map[string]any{"number": 3, "title": "Dark mode", "body": "Please.", "user": forgeUser("stranger"), "state": "open",
		"created_at": at(3), "updated_at": at(3), "labels": label("feature"), "milestone": map[string]any{"title": "v2"},
		"assignees": []any{forgeUser("alice"), forgeUser("stranger")}, "comments": 0}
	pullB := map[string]any{"number": 2, "title": "Add feature", "body": "Adds it.", "user": forgeUser("alice"), "state": "closed",
		"created_at": at(1), "updated_at": at(2), "closed_at": at(2), "merged_at": at(2), "merged": true, "merged_by": forgeUser("alice"), "merge_commit_sha": f.main,
		"head":   map[string]any{"ref": "feature", "sha": f.feature, "repo": map[string]any{"full_name": "alice/source", "owner": forgeUser("alice")}},
		"base":   map[string]any{"ref": "main", "sha": f.main, "repo": map[string]any{"full_name": "alice/source", "owner": forgeUser("alice")}},
		"labels": label("feature")}
	pullD := map[string]any{"number": 4, "title": "Fix typo", "body": "From my fork.", "user": forgeUser("stranger"), "state": "open",
		"created_at": at(4), "updated_at": at(4),
		"head":      map[string]any{"ref": "patch", "sha": f.patch, "repo": map[string]any{"full_name": "octo/source", "owner": forgeUser("octo")}},
		"base":      map[string]any{"ref": "main", "sha": f.main, "repo": map[string]any{"full_name": "alice/source", "owner": forgeUser("alice")}},
		"assignees": []any{forgeUser("alice")}}
	switch path {
	case "/labels":
		return []any{map[string]any{"name": "bug", "color": "d73a4a", "description": "Something broken"}, map[string]any{"name": "feature", "color": "#0E8A16"}}
	case "/milestones":
		return []any{map[string]any{"title": "v1", "state": "closed", "closed_at": at(5), "created_at": at(1)},
			map[string]any{"title": "v2", "description": "Next", "state": "open", "due_on": "2027-01-01T00:00:00Z", "created_at": at(1)}}
	case "/issues":
		if q.Get("per_page") == "1" || q.Get("limit") == "1" { // the newest
			if q.Get("type") == "issues" {
				return []any{issueC}
			}
			return []any{map[string]any{"number": 4, "pull_request": map[string]any{}}}
		}
		if q.Get("page") == "2" {
			return []any{issueC}
		}
		next := *r.URL
		next.Scheme, next.Host = "http", r.Host
		nq := next.Query()
		nq.Set("page", "2")
		next.RawQuery = nq.Encode()
		w.Header().Set("Link", `<`+next.String()+`>; rel="next", <`+next.String()+`>; rel="last"`)
		page := []any{issueA}
		if !gitea {
			page = append(page, map[string]any{"number": 2, "title": "Add feature", "pull_request": map[string]any{}})
		}
		return page
	case "/issues/1/comments":
		return []any{map[string]any{"user": forgeUser("alice"), "body": "Confirmed.", "created_at": at(1), "updated_at": at(1)},
			map[string]any{"user": forgeUser("stranger"), "body": "Me too.", "created_at": at(2)}}
	case "/issues/2/comments":
		return []any{map[string]any{"user": forgeUser("stranger"), "body": "Nice!", "created_at": at(2)}}
	case "/issues/3/comments", "/issues/4/comments", "/pulls/4/comments":
		return []any{}
	case "/pulls":
		if f.failPulls.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return []byte{}
		}
		return []any{pullB, pullD}
	case "/pulls/2/reviews":
		return []any{map[string]any{"id": 10, "user": forgeUser("alice"), "state": "APPROVED", "body": "LGTM", "commit_id": f.feature, "submitted_at": at(2)}}
	case "/pulls/2/comments":
		return []any{map[string]any{"user": forgeUser("alice"), "body": "typo?", "path": "f.txt", "line": 1, "pull_request_review_id": 10, "created_at": at(2)}}
	case "/pulls/2/reviews/10/comments":
		return []any{map[string]any{"user": forgeUser("alice"), "body": "typo?", "path": "f.txt", "position": 1, "created_at": at(2)}}
	case "/pulls/4/reviews":
		changes := "CHANGES_REQUESTED"
		if gitea {
			changes = "REQUEST_CHANGES"
		}
		return []any{map[string]any{"id": 11, "user": forgeUser("stranger"), "state": "APPROVED", "body": "", "commit_id": f.patch, "submitted_at": at(4)},
			map[string]any{"id": 12, "user": forgeUser("alice"), "state": changes, "body": "Please fix the title.", "commit_id": f.patch, "submitted_at": at(5)}}
	case "/pulls/4/reviews/11/comments", "/pulls/4/reviews/12/comments":
		return []any{}
	case "/releases":
		base := "http://" + r.Host
		app := map[string]any{"name": "app.bin", "content_type": "application/x-binary", "size": len(f.asset)}
		huge := map[string]any{"name": "huge.iso", "content_type": "application/octet-stream", "size": 10 << 30}
		if gitea {
			app["browser_download_url"], huge["browser_download_url"] = base+"/attachments/app", base+"/attachments/huge"
		} else {
			app["url"], huge["url"] = base+"/repos/alice/source/releases/assets/1", base+"/repos/alice/source/releases/assets/2"
		}
		return []any{map[string]any{"tag_name": "v1.0.0", "target_commitish": "main", "name": "First", "body": "Notes.", "author": forgeUser("stranger"),
			"created_at": at(6), "published_at": at(6), "assets": []any{app, huge}}}
	case "/releases/assets/1":
		if r.Header.Get("Accept") != "application/octet-stream" {
			return map[string]any{"name": "app.bin"} // GitHub answers metadata without it
		}
		return f.asset
	}
	if path == "/attachments/app" || r.URL.Path == "/attachments/app" {
		return f.asset
	}
	return nil
}

func (f *fakeForge) gitlab(w http.ResponseWriter, r *http.Request, path string) any {
	q := r.URL.Query()
	u := forgeUser
	labels := func(names ...string) []any {
		out := []any{}
		for _, n := range names {
			out = append(out, n)
		}
		return out
	}
	issueA := map[string]any{"iid": 1, "title": "Crash on start", "description": "It crashes.", "author": u("alice"), "state": "closed",
		"created_at": at(1), "updated_at": at(2), "closed_at": at(2), "labels": labels("bug"), "milestone": map[string]any{"title": "v1"}, "user_notes_count": 2}
	issueC := map[string]any{"iid": 2, "title": "Dark mode", "description": "Please.", "author": u("stranger"), "state": "opened",
		"created_at": at(3), "updated_at": at(3), "labels": labels("feature"), "milestone": map[string]any{"title": "v2"},
		"assignees": []any{u("alice"), u("stranger")}, "user_notes_count": 0}
	mrB := map[string]any{"iid": 1, "title": "Add feature", "description": "Adds it.", "author": u("alice"), "state": "merged",
		"created_at": at(1), "updated_at": at(2), "merged_at": at(2), "merge_user": u("alice"), "merge_commit_sha": f.main,
		"source_branch": "feature", "target_branch": "main", "source_project_id": 7, "target_project_id": 7, "sha": f.feature, "labels": labels("feature")}
	mrD := map[string]any{"iid": 2, "title": "Fix typo", "description": "From my fork.", "author": u("stranger"), "state": "opened",
		"created_at": at(4), "updated_at": at(4), "source_branch": "patch", "target_branch": "main", "source_project_id": 8, "target_project_id": 7,
		"sha": f.patch, "assignees": []any{u("alice")}}
	switch path {
	case "/labels":
		return []any{map[string]any{"name": "bug", "color": "#d73a4a", "description": "Something broken"}, map[string]any{"name": "feature", "color": "#0e8a16"}}
	case "/milestones":
		return []any{map[string]any{"title": "v1", "state": "closed", "created_at": at(1), "updated_at": at(5)},
			map[string]any{"title": "v2", "description": "Next", "state": "active", "due_date": "2027-01-01", "created_at": at(1), "updated_at": at(1)}}
	case "/issues":
		if q.Get("per_page") == "1" {
			return []any{issueC}
		}
		if q.Get("page") == "2" {
			return []any{issueC}
		}
		w.Header().Set("Link", `<http://`+r.Host+r.URL.EscapedPath()+`?page=2&per_page=100>; rel="next"`)
		return []any{issueA}
	case "/issues/1/notes":
		return []any{map[string]any{"author": u("alice"), "body": "Confirmed.", "created_at": at(1)},
			map[string]any{"author": u("alice"), "body": "changed the description", "system": true, "created_at": at(1)},
			map[string]any{"author": u("stranger"), "body": "Me too.", "created_at": at(2)}}
	case "/issues/2/notes":
		return []any{}
	case "/merge_requests":
		if q.Get("per_page") == "1" {
			return []any{mrD}
		}
		if f.failPulls.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return []byte{}
		}
		return []any{mrB, mrD}
	case "/merge_requests/1":
		mrB["diff_refs"] = map[string]any{"base_sha": f.main, "head_sha": f.feature}
		return mrB
	case "/merge_requests/2":
		mrD["diff_refs"] = map[string]any{"base_sha": f.main, "head_sha": f.patch}
		return mrD
	case "/merge_requests/1/notes":
		return []any{map[string]any{"author": u("stranger"), "body": "Nice!", "created_at": at(2)},
			map[string]any{"author": u("alice"), "body": "typo?", "type": "DiffNote", "created_at": at(2), "position": map[string]any{"new_path": "f.txt", "new_line": 1}}}
	case "/merge_requests/1/approvals":
		return map[string]any{"approved_by": []any{map[string]any{"user": u("alice")}}}
	case "/merge_requests/2/notes":
		return []any{}
	case "/merge_requests/2/approvals":
		return map[string]any{"approved_by": []any{map[string]any{"user": u("stranger")}}}
	case "/releases":
		return []any{map[string]any{"tag_name": "v1.0.0", "name": "First", "description": "Notes.", "author": u("stranger"), "created_at": at(6), "released_at": at(6),
			"assets": map[string]any{"links": []any{map[string]any{"name": "app.bin", "url": "https://example.com/app.bin"}}}}}
	}
	return nil
}

func TestAPIImport(t *testing.T) {
	for _, kind := range []string{forge.GitHub, forge.GitLab, forge.Gitea} {
		t.Run(kind, func(t *testing.T) { testAPIImport(t, kind) })
	}
	t.Run("github_s3_fake", func(t *testing.T) { testAPIImport(t, forge.GitHub, withS3(s3test.StartFake(t))) })
}

// importSource sets alice/source up: main, feature, tag v1.0.0, and a
// pull request head only in refs/pull/4/head (GitLab:
// refs/merge-requests/2/head).
func importSource(t *testing.T, srv *testServer, alice user, kind string) (main, feature, patch string) {
	srv.createRepo(t, alice.session, "Source", false)
	src := srv.url + "/alice/source.git"
	main = pushFiles(t, src, alice.git, "main", "one", map[string]string{"a.txt": "1\n"})
	feature = pushFiles(t, src, alice.git, "feature", "f", map[string]string{"f.txt": "f\n"})
	pushTag(t, src, alice, "main", "v1.0.0")
	patch = pushFiles(t, src, alice.git, "patch", "p", map[string]string{"a.txt": "patched\n"})
	path := "alice/source"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	st, err := srv.deps.Storage.GetStorer(repo.StorageKey)
	require.NoError(t, err)
	ref := "refs/pull/4/head"
	if kind == forge.GitLab {
		ref = "refs/merge-requests/2/head"
	}
	require.NoError(t, st.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), plumbing.NewHash(patch))))
	deleteBranch(t, src, alice.git, "patch")
	return main, feature, patch
}

func (s *testServer) runImports(t *testing.T) {
	t.Helper()
	for s.deps.ImportApp.RunDue(context.Background()) > 0 {
	}
}

func testAPIImport(t *testing.T, kind string, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, allowLoopbackMirrors, func(c *config.Config) { c.Releases.MaxAssetSize = 1 << 20 })...)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	main, feature, patch := importSource(t, srv, alice, kind)
	fake := startFakeForge(t, kind, main, feature, patch)

	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": srv.url + "/alice/source.git",
		"forge": kind, "api_url": fake.srv.URL, "api_token": forgeToken})
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, imp := srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	assert.Equal(t, "waiting", imp["status"], "for the git data")
	srv.runImports(t)
	assert.Zero(t, fake.calls.Load(), "nothing before the git data")

	srv.syncMirrors(t)
	srv.runImports(t)
	status, imp = srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	require.Equal(t, http.StatusOK, status, "%v", imp)
	require.Equal(t, "done", imp["status"], "%v", imp)
	assert.EqualValues(t, 2, imp["issues"])
	assert.EqualValues(t, 2, imp["pulls"])
	assert.EqualValues(t, 1, imp["releases"])

	gl := kind == forge.GitLab
	name := forge.Name(kind)

	// Issues: numbers, authors, comments, labels, milestones, assignees.
	status, a := srv.get(t, repoAPI("bob/copy")+"/issues/1", bob.session)
	require.Equal(t, http.StatusOK, status, "%v", a)
	assert.Equal(t, "Crash on start", a["title"])
	require.IsType(t, map[string]any{}, a["milestone"])
	assert.Equal(t, "closed", a["state"])
	assert.Equal(t, "alice", a["author"], "matched by user name")
	assert.Equal(t, "It crashes.", a["body"], "no note for matched authors")
	assert.Equal(t, "bug", a["labels"].([]any)[0].(map[string]any)["name"])
	assert.Equal(t, "v1", a["milestone"].(map[string]any)["title"])
	created, err := time.Parse(time.RFC3339, a["created_at"].(string))
	require.NoError(t, err)
	assert.True(t, created.Equal(time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)), "the original date: %v", created)
	_, cs := srv.get(t, repoAPI("bob/copy")+"/issues/1/comments", bob.session)
	comments := cs["comments"].([]any)
	require.Len(t, comments, 2, "system notes skipped")
	assert.Equal(t, "alice", comments[0].(map[string]any)["author"])
	assert.Equal(t, "bob", comments[1].(map[string]any)["author"], "unmatched: the importer")
	assert.Equal(t, "*Originally by @stranger on "+name+", 2024-03-02*\n\nMe too.", comments[1].(map[string]any)["body"])

	cNumber := 3
	if gl {
		cNumber = 2
	}
	_, c := srv.get(t, repoAPI("bob/copy")+"/issues/"+strconv.Itoa(cNumber), bob.session)
	assert.Equal(t, "open", c["state"])
	assert.Equal(t, "bob", c["author"])
	assert.True(t, strings.HasPrefix(c["body"].(string), "*Originally by @stranger on "+name+", 2024-03-03*"), c["body"])
	assert.Equal(t, []any{"alice"}, c["assignees"], "unmatched assignees dropped")
	require.IsType(t, map[string]any{}, c["milestone"])
	assert.Equal(t, "v2", c["milestone"].(map[string]any)["title"])

	// Pull requests.
	bNumber, dNumber := 2, 4
	if gl {
		bNumber, dNumber = 3, 4 // after the issues
	}
	_, b := srv.get(t, repoAPI("bob/copy")+"/pulls/"+strconv.Itoa(bNumber), bob.session)
	assert.Equal(t, "merged", b["state"], "%v", b)
	assert.Equal(t, "alice", b["merged_by"])
	assert.Equal(t, feature, b["head_commit"])
	if gl {
		assert.True(t, strings.HasPrefix(b["body"].(string), "*Originally !1 on GitLab*"), b["body"])
	}
	_, act := srv.get(t, repoAPI("bob/copy")+"/pulls/"+strconv.Itoa(bNumber)+"/activity", bob.session)
	var bodies []string
	for _, x := range act["comments"].([]any) {
		bodies = append(bodies, x.(map[string]any)["body"].(string))
	}
	assert.Contains(t, bodies, "*Originally by @stranger on "+name+", 2024-03-02*\n\nNice!")
	assert.Contains(t, bodies, "On `f.txt` line 1:\n\ntypo?", "inline comments quote their place")
	reviews := act["reviews"].([]any)
	require.NotEmpty(t, reviews)
	assert.Equal(t, "approved", reviews[0].(map[string]any)["state"])
	assert.Equal(t, "alice", reviews[0].(map[string]any)["author"])

	_, d := srv.get(t, repoAPI("bob/copy")+"/pulls/"+strconv.Itoa(dNumber), bob.session)
	assert.Equal(t, "open", d["state"], "%v", d)
	assert.Equal(t, patch, d["head_commit"], "the fetched head")
	assert.Contains(t, d["source_branch"], ":patch", "from a fork: matches no branch")
	assert.Equal(t, false, d["branches_exist"])
	assert.Equal(t, false, d["can_merge"], "read-only")
	assert.Empty(t, d["approved"], "the stranger's approval isn't the importer's")
	_, act = srv.get(t, repoAPI("bob/copy")+"/pulls/"+strconv.Itoa(dNumber)+"/activity", bob.session)
	found := false
	for _, x := range act["reviews"].([]any) {
		r := x.(map[string]any)
		if strings.Contains(r["body"].(string), "Originally approved by @stranger") {
			found = true
			assert.Equal(t, "commented", r["state"])
		}
	}
	assert.True(t, found, "%v", act["reviews"])
	status, cmp := srv.get(t, repoAPI("bob/copy")+"/pulls/"+strconv.Itoa(dNumber)+"/compare", bob.session)
	assert.Equal(t, http.StatusOK, status, "the diff works: %v", cmp)
	refs := srv.refsOf(t, "bob/copy")
	assert.NotContains(t, refs, "refs/heads/patch")

	// Releases.
	_, rels := srv.get(t, repoAPI("bob/copy")+"/releases", bob.session)
	require.Len(t, rels["releases"], 1)
	rel := rels["releases"].([]any)[0].(map[string]any)
	assert.Equal(t, "v1.0.0", rel["tag_name"])
	assert.Equal(t, false, rel["draft"])
	if gl {
		assert.Contains(t, rel["body"], "[app.bin](https://example.com/app.bin)", "GitLab's links stay links")
	} else {
		assets := rel["assets"].([]any)
		require.Len(t, assets, 1, "the too-large one is skipped")
		asset := assets[0].(map[string]any)
		assert.Equal(t, "app.bin", asset["name"])
		path := "bob/copy"
		repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
		require.NoError(t, err)
		r, err := srv.deps.Storage.OpenAsset(repo.StorageKey, asset["id"].(string))
		require.NoError(t, err)
		got, _ := io.ReadAll(r)
		r.Close()
		assert.Equal(t, fake.asset, got)
		assert.Contains(t, imp["warnings"], "release v1.0.0: huge.iso is larger than releases.max_asset_size, skipped")
	}

	// New issues come after the imported numbers; the token is forgotten.
	status, n := srv.post(t, repoAPI("bob/copy")+"/issues", bob.session, map[string]any{"title": "New"})
	require.Equal(t, http.StatusOK, status, "%v", n)
	assert.EqualValues(t, 5, n["number"])
	path := "bob/copy"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	row, err := srv.deps.ImportApp.Pers.ForRepository(repo.Id)
	require.NoError(t, err)
	assert.Empty(t, row.Secret)
}

// TestAPIImportResumes: a rate limit pauses the import until the reset, a
// failure retries later, and what was imported isn't imported twice.
func TestAPIImportResumes(t *testing.T) {
	srv := startTestServer(t, allowLoopbackMirrors)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	main, feature, patch := importSource(t, srv, alice, forge.GitHub)
	fake := startFakeForge(t, forge.GitHub, main, feature, patch)
	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": srv.url + "/alice/source.git",
		"forge": "github", "api_url": fake.srv.URL, "api_token": forgeToken})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)

	fake.rateLimit.Store(true)
	fake.rateLimitedUntil = time.Now().Add(time.Hour).Truncate(time.Second)
	srv.runImports(t)
	_, imp := srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	assert.Equal(t, "pending", imp["status"])
	assert.Contains(t, imp["last_error"], "rate limit")
	next, err := time.Parse(time.RFC3339Nano, imp["next_run_at"].(string))
	require.NoError(t, err)
	assert.True(t, !next.Before(fake.rateLimitedUntil), "waits for the reset: %v", next)

	fake.rateLimit.Store(false)
	fake.failPulls.Store(true)
	later := time.Now().Add(2 * time.Hour)
	srv.deps.ImportApp.Now = func() time.Time { return later.UTC() }
	srv.runImports(t)
	_, imp = srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	assert.Equal(t, "pending", imp["status"], "%v", imp)
	assert.Equal(t, "pulls", imp["stage"])
	assert.EqualValues(t, 2, imp["issues"])
	assert.NotEmpty(t, imp["last_error"])

	// Meanwhile, an issue opened here: after the reserved numbers.
	status, n := srv.post(t, repoAPI("bob/copy")+"/issues", bob.session, map[string]any{"title": "Meanwhile"})
	require.Equal(t, http.StatusOK, status, "%v", n)
	assert.EqualValues(t, 5, n["number"])

	fake.failPulls.Store(false)
	later = later.Add(time.Hour)
	srv.runImports(t)
	_, imp = srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	require.Equal(t, "done", imp["status"], "%v", imp)
	assert.EqualValues(t, 2, imp["issues"], "not twice")
	assert.EqualValues(t, 2, imp["pulls"])
	_, list := srv.get(t, repoAPI("bob/copy")+"/issues?state=all", bob.session)
	assert.Len(t, list["issues"], 3)
	_, d := srv.get(t, repoAPI("bob/copy")+"/pulls/4", bob.session)
	assert.Equal(t, "Fix typo", d["title"], "its number was kept for it")

	// Issues and pull requests share numbers: an item never takes one the
	// other kind has.
	path := "bob/copy"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	added, err := srv.deps.ImportApp.Pers.AddPull(&domain.PullRequest{RepositoryId: repo.Id, Number: 1, Title: "x", SourceBranch: "a", TargetBranch: "main",
		State: domain.PullRequestClosed}, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, added, "#1 is an issue")
	added, err = srv.deps.ImportApp.Pers.AddIssue(&domain.Issue{RepositoryId: repo.Id, Number: 4, Title: "x", State: domain.IssueOpen}, nil, nil, nil)
	require.NoError(t, err)
	assert.False(t, added, "#4 is a pull request")
}

func TestAPIImportPermissionsAndInput(t *testing.T) {
	srv := startTestServer(t, allowLoopbackMirrors)
	alice, bob, carol := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol")
	main, feature, patch := importSource(t, srv, alice, forge.GitHub)
	fake := startFakeForge(t, forge.GitHub, main, feature, patch)
	src := srv.url + "/alice/source.git"

	for _, bad := range []map[string]any{
		{"name": "X", "url": src, "forge": "bitbucket"},
		{"name": "X", "url": src, "forge": "github", "api_url": "ftp://example.com"},
	} {
		status, body := srv.post(t, "/api/v1/repository/import", bob.session, bad)
		assert.Equal(t, http.StatusBadRequest, status, "%v", bad)
		assert.Equal(t, "INVALID_IMPORT", body["type"])
	}

	// A wrong token: the import fails with a clear error, then is retried.
	status, body := srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Copy", "url": src, "forge": "github",
		"api_url": fake.srv.URL, "api_token": "wrong"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.syncMirrors(t)
	srv.runImports(t)
	_, imp := srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	assert.Contains(t, imp["last_error"], "refused the API token")

	status, _ = srv.post(t, repoAPI("bob/copy")+"/members", bob.session, map[string]any{"username": "carol", "role": "developer"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.get(t, repoAPI("bob/copy")+"/import", carol.session)
	assert.Equal(t, http.StatusForbidden, status, "maintainers and owners only")
	status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/copy")+"/import", carol.session, nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = srv.post(t, repoAPI("bob/copy")+"/import/retry", bob.session, nil)
	assert.Equal(t, http.StatusConflict, status, "not failed yet (it retries by itself)")

	path := "bob/copy"
	repo, err := srv.deps.RepoApp.GetRepository(nil, &path)
	require.NoError(t, err)
	row, err := srv.deps.ImportApp.Pers.ForRepository(repo.Id)
	require.NoError(t, err)
	require.NoError(t, srv.deps.ImportApp.Pers.Update(row.Id, map[string]any{"status": "failed"}))
	status, imp = srv.post(t, repoAPI("bob/copy")+"/import/retry", bob.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", imp)
	assert.Equal(t, "pending", imp["status"])

	status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/copy")+"/import", bob.session, nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.get(t, repoAPI("bob/copy")+"/import", bob.session)
	assert.Equal(t, http.StatusNotFound, status)

	// Deleting the repository takes its import.
	status, body = srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Gone", "url": src, "forge": "github", "api_url": fake.srv.URL})
	require.Equal(t, http.StatusOK, status, "%v", body)
	gone := "bob/gone"
	goneRepo, err := srv.deps.RepoApp.GetRepository(nil, &gone)
	require.NoError(t, err)
	status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/gone")+"?confirm=gone", bob.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, err = srv.deps.ImportApp.Pers.ForRepository(goneRepo.Id)
	assert.Error(t, err)

	// Without an encryption key, a token can't be kept.
	srv.deps.ImportApp.Box = nil
	status, body = srv.post(t, "/api/v1/repository/import", bob.session, map[string]any{"name": "Other", "url": src, "forge": "github", "api_token": "x"})
	assert.Equal(t, http.StatusConflict, status, "%v", body)
}
