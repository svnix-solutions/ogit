package server

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func issuesAPI(repo string, rest ...any) string {
	s := repoAPI(repo) + "/issues"
	for _, r := range rest {
		s += fmt.Sprintf("/%v", r)
	}
	return s
}

func (s *testServer) openIssue(t *testing.T, as user, repo, title string) map[string]any {
	t.Helper()
	status, body := s.post(t, issuesAPI(repo), as.session, map[string]any{"title": title, "body": "about " + title})
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

func TestIssueNumbersAreSharedWithPullRequests(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv

	status, pr := f.open(t, f.dev, "one", map[string]string{"a.txt": "a\n"}, nil)
	require.Equal(t, http.StatusOK, status, "%v", pr)
	assert.EqualValues(t, 1, pr["number"])
	assert.EqualValues(t, 2, srv.openIssue(t, f.reporter, "owner/app", "Bug")["number"])
	status, pr = f.open(t, f.dev, "two", map[string]string{"b.txt": "b\n"}, nil)
	require.Equal(t, http.StatusOK, status, "%v", pr)
	assert.EqualValues(t, 3, pr["number"], "after the issue")

	for n, want := range map[int]string{1: "pull", 2: "issue", 3: "pull"} {
		status, body := srv.get(t, repoAPI("owner/app")+fmt.Sprintf("/numbers/%d", n), "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, want, body["type"], "#%d", n)
	}
	status, _ = srv.get(t, repoAPI("owner/app")+"/numbers/99", "")
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = srv.get(t, issuesAPI("owner/app", 1), "")
	assert.Equal(t, http.StatusNotFound, status, "#1 is a pull request")

	// Concurrent issues and pull requests never get the same number.
	for i := range 6 {
		pushFiles(t, f.url, f.dev.git, fmt.Sprintf("c%d", i), "c", map[string]string{fmt.Sprintf("c%d.txt", i): "c\n"})
	}
	var mu sync.Mutex
	var numbers []int
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var status int
			var body map[string]any
			if i%2 == 0 {
				status, body = srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "c", "source": fmt.Sprintf("c%d", i/2)})
			} else {
				status, body = srv.post(t, issuesAPI("owner/app"), f.reporter.session, map[string]any{"title": "c"})
			}
			if assert.Equal(t, http.StatusOK, status, "%v", body) {
				mu.Lock()
				numbers = append(numbers, int(body["number"].(float64)))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	sort.Ints(numbers)
	assert.Equal(t, []int{4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, numbers, "unique and without gaps")
}

func TestIssuePermissions(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	issue := srv.openIssue(t, f.reporter, "owner/app", "Login is broken")
	n := issue["number"]
	assert.Equal(t, "open", issue["state"])
	assert.Equal(t, "reporter", issue["author"])
	assert.Equal(t, true, issue["can_edit"], "authors edit their issues")
	assert.Equal(t, false, issue["can_label"])

	t.Run("private_repositories_stay_hidden", func(t *testing.T) {
		status, _ := srv.post(t, issuesAPI("owner/app"), f.outer.session, map[string]any{"title": "x"})
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.get(t, issuesAPI("owner/app", n), "")
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.get(t, issuesAPI("owner/app"), f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("edit_and_close", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.reporter.session, map[string]any{"title": "Login is broken on Safari"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Login is broken on Safari", body["title"])

		other := srv.newUser(t, "other")
		status, _ = srv.post(t, repoAPI("owner/app")+"/members", f.owner.session, map[string]any{"username": "other", "role": "reporter"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), other.session, map[string]any{"state": "closed"})
		assert.Equal(t, http.StatusForbidden, status, "other readers don't close it")
		status, body = srv.get(t, issuesAPI("owner/app", n), other.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["can_edit"])
		assert.Equal(t, true, body["can_comment"])

		status, body = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.dev.session, map[string]any{"state": "closed"})
		require.Equal(t, http.StatusOK, status, "developers do: %v", body)
		assert.Equal(t, "closed", body["state"])
		assert.Equal(t, "dev", body["closed_by"])
		assert.NotNil(t, body["closed_at"])
		status, body = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.reporter.session, map[string]any{"state": "open"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "open", body["state"])
		assert.Nil(t, body["closed_at"])
		status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.dev.session, map[string]any{"state": "done"})
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.dev.session, map[string]any{"title": " "})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("comments", func(t *testing.T) {
		status, c := srv.post(t, issuesAPI("owner/app", n, "comments"), f.dev.session, map[string]any{"body": "Can't reproduce"})
		require.Equal(t, http.StatusOK, status, "%v", c)
		id := c["id"].(string)
		status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n, "comments", id), f.reporter.session, map[string]any{"body": "x"})
		assert.Equal(t, http.StatusForbidden, status, "only the author edits")
		status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", n, "comments", id), f.dev.session, map[string]any{"body": "Reproduced"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodDelete, issuesAPI("owner/app", n, "comments", id), f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status)

		status, body := srv.get(t, issuesAPI("owner/app", n, "comments"), f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		comments := body["comments"].([]any)
		require.Len(t, comments, 1)
		assert.Equal(t, "Reproduced", comments[0].(map[string]any)["body"])
		assert.Equal(t, false, comments[0].(map[string]any)["can_edit"])

		status, _ = srv.request(t, http.MethodDelete, issuesAPI("owner/app", n, "comments", id), f.maint.session, nil)
		assert.Equal(t, http.StatusOK, status, "maintainers delete")
		status, _ = srv.post(t, issuesAPI("owner/app", n, "comments"), f.dev.session, map[string]any{"body": " "})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("labels", func(t *testing.T) {
		status, _ := srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": "bug", "color": "#d73a4a"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "labels"), f.reporter.session, map[string]any{"labels": []string{"bug"}})
		assert.Equal(t, http.StatusForbidden, status, "developers and up")
		status, body := srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "labels"), f.dev.session, map[string]any{"labels": []string{"bug"}})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "labels"), f.dev.session, map[string]any{"labels": []string{"nope"}})
		assert.Equal(t, http.StatusNotFound, status)
		_, body = srv.get(t, issuesAPI("owner/app", n), f.reporter.session)
		assert.Equal(t, "bug", body["labels"].([]any)[0].(map[string]any)["name"])
	})

	t.Run("delete", func(t *testing.T) {
		doomed := srv.openIssue(t, f.reporter, "owner/app", "Spam")
		status, _ := srv.request(t, http.MethodDelete, issuesAPI("owner/app", doomed["number"]), f.dev.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, issuesAPI("owner/app", doomed["number"]), f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "not even the author")
		status, _ = srv.request(t, http.MethodDelete, issuesAPI("owner/app", doomed["number"]), f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, issuesAPI("owner/app", doomed["number"]), f.maint.session)
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestIssuesOnPublicRepositories(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	issue := srv.openIssue(t, f.outer, "owner/app", "Docs typo")
	assert.Equal(t, "outer", issue["author"], "any signed-in user opens issues on public repositories")
	status, body := srv.get(t, issuesAPI("owner/app", issue["number"]), "")
	require.Equal(t, http.StatusOK, status, "anonymous readers read")
	assert.Equal(t, false, body["can_comment"])
	status, _ = srv.post(t, issuesAPI("owner/app"), "", map[string]any{"title": "x"})
	assert.NotEqual(t, http.StatusOK, status)
}

func TestIssueFilters(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	status, _ := srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": "bug", "color": "#d73a4a"})
	require.Equal(t, http.StatusOK, status)
	a := srv.openIssue(t, f.reporter, "owner/app", "Crash on START")
	srv.openIssue(t, f.dev, "owner/app", "100% CPU")
	c := srv.openIssue(t, f.reporter, "owner/app", "Slow page")
	srv.request(t, http.MethodPut, issuesAPI("owner/app", a["number"], "labels"), f.dev.session, map[string]any{"labels": []string{"bug"}})
	srv.request(t, http.MethodPatch, issuesAPI("owner/app", c["number"]), f.dev.session, map[string]any{"state": "closed"})

	titles := func(query string) ([]string, map[string]any) {
		status, body := srv.get(t, issuesAPI("owner/app")+query, "")
		require.Equal(t, http.StatusOK, status, "%s: %v", query, body)
		var out []string
		for _, is := range body["issues"].([]any) {
			out = append(out, is.(map[string]any)["title"].(string))
		}
		return out, body["counts"].(map[string]any)
	}
	got, counts := titles("")
	assert.Equal(t, []string{"100% CPU", "Crash on START"}, got, "open, newest first")
	assert.EqualValues(t, map[string]any{"open": 2.0, "closed": 1.0}, counts)
	got, _ = titles("?state=closed")
	assert.Equal(t, []string{"Slow page"}, got)
	got, _ = titles("?state=all&author=reporter")
	assert.Equal(t, []string{"Slow page", "Crash on START"}, got)
	got, _ = titles("?label=bug")
	assert.Equal(t, []string{"Crash on START"}, got)
	got, _ = titles("?label=nope")
	assert.Empty(t, got)
	got, _ = titles("?author=nobody")
	assert.Empty(t, got)
	got, counts = titles("?q=start")
	assert.Equal(t, []string{"Crash on START"}, got, "case-insensitive")
	assert.EqualValues(t, map[string]any{"open": 1.0, "closed": 0.0}, counts, "counts follow the filters")
	got, _ = titles("?state=all&q=" + "%25")
	assert.Equal(t, []string{"100% CPU"}, got, "% is literal")
	got, _ = titles("?state=all&q=about+slow")
	assert.Equal(t, []string{"Slow page"}, got, "bodies too")
	status, _ = srv.get(t, issuesAPI("owner/app")+"?state=weird", "")
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestIssuesCanBeTurnedOff(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	issue := srv.openIssue(t, f.reporter, "owner/app", "Old")
	status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.maint.session, map[string]any{"has_issues": false})
	assert.Equal(t, http.StatusForbidden, status, "owners only")
	status, body := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"has_issues": false})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, false, body["has_issues"])
	_, body = srv.get(t, repoAPI("owner/app"), "")
	assert.Equal(t, false, body["has_issues"])

	status, body = srv.post(t, issuesAPI("owner/app"), f.reporter.session, map[string]any{"title": "New"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "ISSUES_DISABLED", body["type"])
	status, _ = srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.reporter.session, map[string]any{"body": "x"})
	assert.Equal(t, http.StatusConflict, status)
	status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", issue["number"]), f.owner.session, map[string]any{"state": "closed"})
	assert.Equal(t, http.StatusConflict, status, "read-only")
	status, body = srv.get(t, issuesAPI("owner/app", issue["number"]), f.owner.session)
	require.Equal(t, http.StatusOK, status, "still readable")
	assert.Equal(t, false, body["can_comment"])
	assert.Equal(t, false, body["can_edit"])

	status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"has_issues": true})
	require.Equal(t, http.StatusOK, status)
	srv.openIssue(t, f.reporter, "owner/app", "Back")

	// New repositories have issues.
	srv.createRepo(t, f.owner.session, "Fresh", false)
	_, fresh := srv.get(t, repoAPI("owner/fresh"), "")
	assert.Equal(t, true, fresh["has_issues"])
}

func TestIssuesAreCleanedUp(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	status, _ := srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": "bug", "color": "#d73a4a"})
	require.Equal(t, http.StatusOK, status)
	issue := srv.openIssue(t, f.outer, "owner/app", "By outer")
	n := issue["number"]
	srv.post(t, issuesAPI("owner/app", n, "comments"), f.outer.session, map[string]any{"body": "me too"})
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "labels"), f.dev.session, map[string]any{"labels": []string{"bug"}})

	// Deleting a label removes it from issues.
	_, labels := srv.get(t, repoAPI("owner/app")+"/labels", "")
	labelId := labels["labels"].([]any)[0].(map[string]any)["id"].(string)
	status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"/labels/"+labelId, f.owner.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body := srv.get(t, issuesAPI("owner/app", n), "")
	assert.Empty(t, body["labels"])
	var left int64
	require.NoError(t, srv.deps.Database.Db.Raw("SELECT COUNT(*) FROM issue_label").Scan(&left).Error)
	assert.Zero(t, left, "no rows left behind (SQLite doesn't cascade)")

	// Deleting a user keeps their issues and comments, by a deleted user.
	admin := srv.bootstrapAdmin(t)
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/users/outer", admin.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, issuesAPI("owner/app", n), "")
	assert.Equal(t, "", body["author"])
	_, body = srv.get(t, issuesAPI("owner/app", n, "comments"), "")
	assert.Equal(t, "", body["comments"].([]any)[0].(map[string]any)["author"])

	// Deleting the repository deletes its issues.
	status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"?confirm=app", f.owner.session, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Zero(t, srv.countRows(t, &domain.Issue{}, "1 = 1"))
	assert.Zero(t, srv.countRows(t, &domain.IssueComment{}, "1 = 1"))
}
