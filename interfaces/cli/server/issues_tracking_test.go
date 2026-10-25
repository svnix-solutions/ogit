package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *testServer) milestone(t *testing.T, as user, repo string, body map[string]any) map[string]any {
	t.Helper()
	status, m := s.post(t, repoAPI(repo)+"/milestones", as.session, body)
	require.Equal(t, http.StatusOK, status, "%v", m)
	return m
}

func TestMilestones(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	ms := repoAPI("owner/app") + "/milestones"

	status, _ := srv.post(t, ms, f.dev.session, map[string]any{"title": "v1"})
	assert.Equal(t, http.StatusForbidden, status, "maintainers and up")
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	v1 := srv.milestone(t, f.maint, "owner/app", map[string]any{"title": "v1", "description": "First", "due_on": due})
	assert.Equal(t, "open", v1["state"])
	gotDue, err := time.Parse(time.RFC3339, v1["due_on"].(string))
	require.NoError(t, err)
	assert.True(t, due.Equal(gotDue), "due_on %v", v1["due_on"]) // Postgres answers in local time
	v2 := srv.milestone(t, f.maint, "owner/app", map[string]any{"title": "v2"})
	status, body := srv.post(t, ms, f.maint.session, map[string]any{"title": "v1"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "MILESTONE_EXISTS", body["type"])
	status, _ = srv.post(t, ms, f.maint.session, map[string]any{"title": " "})
	assert.Equal(t, http.StatusBadRequest, status)

	_, body = srv.get(t, ms, f.reporter.session)
	titles := []string{}
	for _, m := range body["milestones"].([]any) {
		titles = append(titles, m.(map[string]any)["title"].(string))
	}
	assert.Equal(t, []string{"v1", "v2"}, titles, "readers see them, soonest due first")
	status, _ = srv.get(t, ms, f.outer.session)
	assert.Equal(t, http.StatusNotFound, status)

	// Progress counts issues and pull requests.
	issue := srv.openIssue(t, f.reporter, "owner/app", "Bug")
	closed := srv.openIssue(t, f.reporter, "owner/app", "Old bug")
	_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	for _, path := range []string{issuesAPI("owner/app", issue["number"], "milestone"), issuesAPI("owner/app", closed["number"], "milestone"), pullsAPI("owner/app", pr["number"], "milestone")} {
		status, body := srv.request(t, http.MethodPut, path, f.dev.session, map[string]any{"milestone_id": v1["id"]})
		require.Equal(t, http.StatusOK, status, "%s: %v", path, body)
		assert.Equal(t, "v1", body["milestone"].(map[string]any)["title"])
	}
	srv.request(t, http.MethodPatch, issuesAPI("owner/app", closed["number"]), f.dev.session, map[string]any{"state": "closed"})
	status, body = srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, m := srv.get(t, ms+"/"+v1["id"].(string), f.reporter.session)
	assert.EqualValues(t, 1, m["open"])
	assert.EqualValues(t, 2, m["closed"], "a closed issue and a merged pull request")

	status, _ = srv.request(t, http.MethodPut, issuesAPI("owner/app", issue["number"], "milestone"), f.reporter.session, map[string]any{"milestone_id": v2["id"]})
	assert.Equal(t, http.StatusForbidden, status, "developers and up")
	srv.createRepo(t, f.owner.session, "Other", true)
	other := srv.milestone(t, f.owner, "owner/other", map[string]any{"title": "elsewhere"})
	status, _ = srv.request(t, http.MethodPut, issuesAPI("owner/app", issue["number"], "milestone"), f.dev.session, map[string]any{"milestone_id": other["id"]})
	assert.Equal(t, http.StatusNotFound, status, "another repository's milestone")

	// Closing, reopening, and deleting.
	status, m = srv.request(t, http.MethodPatch, ms+"/"+v1["id"].(string), f.maint.session, map[string]any{"state": "closed", "clear_due_on": true})
	require.Equal(t, http.StatusOK, status, "%v", m)
	assert.Equal(t, "closed", m["state"])
	assert.NotNil(t, m["closed_at"])
	assert.Nil(t, m["due_on"])
	_, body = srv.get(t, ms+"?state=closed", f.reporter.session)
	assert.Len(t, body["milestones"], 1)
	status, _ = srv.request(t, http.MethodDelete, ms+"/"+v1["id"].(string), f.dev.session, nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = srv.request(t, http.MethodDelete, ms+"/"+v1["id"].(string), f.maint.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, issuesAPI("owner/app", issue["number"]), f.reporter.session)
	assert.Nil(t, body["milestone"], "the issue keeps going without it")
	_, body = srv.get(t, pullsAPI("owner/app", pr["number"]), f.reporter.session)
	assert.Nil(t, body["milestone"])
}

func TestAssignees(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	issue := srv.openIssue(t, f.reporter, "owner/app", "Bug")
	n := issue["number"]
	assign := func(as user, path string, names ...string) (int, map[string]any) {
		return srv.request(t, http.MethodPut, path, as.session, map[string]any{"assignees": names})
	}

	status, body := assign(f.dev, issuesAPI("owner/app", n, "assignees"), "dev", "maint", "dev")
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, []any{"dev", "maint"}, body["assignees"])
	status, _ = assign(f.reporter, issuesAPI("owner/app", n, "assignees"), "dev")
	assert.Equal(t, http.StatusForbidden, status, "developers and up")
	for _, who := range []string{"reporter", "outer", "nobody"} {
		status, body := assign(f.dev, issuesAPI("owner/app", n, "assignees"), who)
		assert.Equal(t, http.StatusBadRequest, status, "%s: %v", who, body)
		assert.Equal(t, "INVALID_ASSIGNEE", body["type"])
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = "dev"
	}
	many[10] = "maint" // 11 entries
	status, _ = assign(f.dev, issuesAPI("owner/app", n, "assignees"), many...)
	assert.Equal(t, http.StatusBadRequest, status, "at most 10")

	_, body = srv.get(t, issuesAPI("owner/app", n), f.reporter.session)
	assert.Equal(t, []any{"dev", "maint"}, body["assignees"])

	_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	status, body = assign(f.dev, pullsAPI("owner/app", pr["number"], "assignees"), "maint")
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, body = srv.get(t, pullsAPI("owner/app", pr["number"]), f.reporter.session)
	assert.Equal(t, []any{"maint"}, body["assignees"])

	// Filters.
	srv.openIssue(t, f.dev, "owner/app", "Unassigned")
	numbers := func(as user, query string) []any {
		status, body := srv.get(t, issuesAPI("owner/app")+query, as.session)
		require.Equal(t, http.StatusOK, status, "%s: %v", query, body)
		var out []any
		for _, is := range body["issues"].([]any) {
			out = append(out, is.(map[string]any)["title"])
		}
		return out
	}
	assert.Equal(t, []any{"Bug"}, numbers(f.reporter, "?assignee=maint"))
	assert.Equal(t, []any{"Unassigned"}, numbers(f.reporter, "?assignee=none"))
	assert.Equal(t, []any{"Bug"}, numbers(f.dev, "?mine=assigned"))
	assert.Equal(t, []any{"Unassigned"}, numbers(f.dev, "?mine=created"))
	assert.Equal(t, []any{"Bug"}, numbers(f.reporter, "?milestone=none&assignee=dev"))
	status, _ = srv.get(t, issuesAPI("owner/app")+"?mine=assigned", "")
	assert.NotEqual(t, http.StatusOK, status, "needs a session")

	// Deleted users are unassigned.
	admin := srv.bootstrapAdmin(t)
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/users/maint", admin.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, issuesAPI("owner/app", n), f.reporter.session)
	assert.Equal(t, []any{"dev"}, body["assignees"])
	var left int64
	require.NoError(t, srv.deps.Database.Db.Raw("SELECT COUNT(*) FROM pull_request_assignee").Scan(&left).Error)
	assert.Zero(t, left)
}

func TestClosingIssuesFromPullRequests(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	fix := srv.openIssue(t, f.reporter, "owner/app", "Crash")
	other := srv.openIssue(t, f.reporter, "owner/app", "Typo")
	untouched := srv.openIssue(t, f.reporter, "owner/app", "Mentioned only")

	assert.Equal(t, []int{12, 3, 7}, application.ClosingReferences("Fixes #12, closes #3. Resolved: #7; fixes #12 again; see #9; prefix#4 fixes#5"))

	body := fmt.Sprintf("Fixes #%v and resolves #%v. See #%v. Fixes #99.", fix["number"], other["number"], untouched["number"])
	status, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, map[string]any{"body": body})
	require.Equal(t, http.StatusOK, status, "%v", pr)

	// Into another branch: nothing closes.
	pushFiles(t, f.url, f.dev.git, "release", "base", nil)
	status, side := f.open(t, f.dev, "side", map[string]string{"s.txt": "s\n"}, map[string]any{"target": "release", "body": "Fixes #" + fmt.Sprint(fix["number"])})
	require.Equal(t, http.StatusOK, status, "%v", side)
	status, _ = srv.post(t, pullsAPI("owner/app", side["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
	require.Equal(t, http.StatusOK, status)
	_, got := srv.get(t, issuesAPI("owner/app", fix["number"]), f.reporter.session)
	assert.Equal(t, "open", got["state"], "only merges into the default branch close issues")

	status, merged := srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "squash"})
	require.Equal(t, http.StatusOK, status, "%v", merged)
	for _, is := range []map[string]any{fix, other} {
		_, got := srv.get(t, issuesAPI("owner/app", is["number"]), f.reporter.session)
		assert.Equal(t, "closed", got["state"], "#%v", is["number"])
		assert.Equal(t, "maint", got["closed_by"])
		assert.EqualValues(t, pr["number"], got["closed_by_pull"])
	}
	_, got = srv.get(t, issuesAPI("owner/app", untouched["number"]), f.reporter.session)
	assert.Equal(t, "open", got["state"], "a mention is not a closing keyword")
}

func TestIssueWebhooks(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback, withPublicURL)
	srv := f.srv
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"issues", "issue_comment", "milestone", "pull_request"}})
	srv.deliver(t)
	rcv.take()
	collect := func() []hookEvent {
		srv.deliver(t)
		return rcv.takeEvents()
	}

	issue := srv.openIssue(t, f.reporter, "owner/app", "Crash")
	n := issue["number"]
	evs := collect()
	require.Equal(t, []string{"issues.opened"}, names(evs))
	is := evs[0].payload["issue"].(map[string]any)
	assert.Equal(t, "Crash", is["title"])
	assert.Equal(t, "https://git.example.com/owner/app/issues/"+fmt.Sprint(n), is["html_url"])
	assert.NotContains(t, is, "pull_request", "an issue, not a pull request")
	assert.Equal(t, []any{}, is["assignees"])

	srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.reporter.session, map[string]any{"title": "Crash on start"})
	evs = collect()
	require.Equal(t, []string{"issues.edited"}, names(evs))
	assert.Equal(t, "Crash", evs[0].payload["changes"].(map[string]any)["title"].(map[string]any)["from"])

	srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": "bug", "color": "#d73a4a"})
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "labels"), f.dev.session, map[string]any{"labels": []string{"bug"}})
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "assignees"), f.dev.session, map[string]any{"assignees": []string{"dev"}})
	evs = collect()
	require.ElementsMatch(t, []string{"issues.labeled", "issues.assigned"}, names(evs))
	assert.Equal(t, "dev", find(t, evs, "issues.assigned").payload["assignee"].(map[string]any)["login"])
	assert.Equal(t, "bug", find(t, evs, "issues.labeled").payload["label"].(map[string]any)["name"])

	m := srv.milestone(t, f.maint, "owner/app", map[string]any{"title": "v1"})
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "milestone"), f.dev.session, map[string]any{"milestone_id": m["id"]})
	evs = collect()
	require.ElementsMatch(t, []string{"milestone.created", "issues.milestoned"}, names(evs))
	milestoned := find(t, evs, "issues.milestoned").payload
	assert.Equal(t, "v1", milestoned["milestone"].(map[string]any)["title"])
	assert.Equal(t, "v1", milestoned["issue"].(map[string]any)["milestone"].(map[string]any)["title"])
	assert.Equal(t, "dev", milestoned["issue"].(map[string]any)["assignee"].(map[string]any)["login"])

	status, c := srv.post(t, issuesAPI("owner/app", n, "comments"), f.dev.session, map[string]any{"body": "On it"})
	require.Equal(t, http.StatusOK, status)
	evs = collect()
	require.Equal(t, []string{"issue_comment.created"}, names(evs))
	assert.Equal(t, "On it", evs[0].payload["comment"].(map[string]any)["body"])
	assert.NotContains(t, evs[0].payload["issue"], "pull_request")
	srv.request(t, http.MethodDelete, issuesAPI("owner/app", n, "comments", c["id"]), f.dev.session, nil)
	assert.Equal(t, []string{"issue_comment.deleted"}, names(collect()))

	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "assignees"), f.dev.session, map[string]any{"assignees": []string{}})
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "milestone"), f.dev.session, map[string]any{"milestone_id": ""})
	assert.ElementsMatch(t, []string{"issues.unassigned", "issues.demilestoned"}, names(collect()))
	srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.dev.session, map[string]any{"state": "closed"})
	srv.request(t, http.MethodPatch, issuesAPI("owner/app", n), f.dev.session, map[string]any{"state": "open"})
	assert.ElementsMatch(t, []string{"issues.closed", "issues.reopened"}, names(collect()))

	srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/milestones/"+m["id"].(string), f.maint.session, map[string]any{"state": "closed", "title": "v1.0"})
	assert.ElementsMatch(t, []string{"milestone.edited", "milestone.closed"}, names(collect()))
	srv.request(t, http.MethodDelete, repoAPI("owner/app")+"/milestones/"+m["id"].(string), f.maint.session, nil)
	assert.Equal(t, []string{"milestone.deleted"}, names(collect()))

	// Pull requests: assignees and milestones, and closing issues by merge.
	m2 := srv.milestone(t, f.maint, "owner/app", map[string]any{"title": "v2"})
	collect()
	_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, map[string]any{"body": "Fixes #" + fmt.Sprint(n)})
	srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "assignees"), f.dev.session, map[string]any{"assignees": []string{"maint"}})
	srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "milestone"), f.dev.session, map[string]any{"milestone_id": m2["id"]})
	evs = collect()
	require.ElementsMatch(t, []string{"pull_request.opened", "pull_request.assigned", "pull_request.milestoned"}, names(evs))
	assert.Equal(t, "maint", find(t, evs, "pull_request.assigned").payload["assignee"].(map[string]any)["login"])
	assert.Equal(t, "v2", find(t, evs, "pull_request.milestoned").payload["pull_request"].(map[string]any)["milestone"].(map[string]any)["title"])
	srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
	evs = collect()
	assert.ElementsMatch(t, []string{"pull_request.closed", "issues.closed"}, names(evs))
	assert.Equal(t, "maint", find(t, evs, "issues.closed").payload["sender"].(map[string]any)["login"])

	srv.request(t, http.MethodDelete, issuesAPI("owner/app", n), f.maint.session, nil)
	assert.Equal(t, []string{"issues.deleted"}, names(collect()))
}

func TestIssueEmails(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.SMTP.Host = "smtp.test"; c.Server.PublicURL = "https://git.example.com" })
	rec := &mailtest.Recorder{}
	f.srv.deps.IssueApp.Mailer = rec
	srv := f.srv
	got := func(who string, contains string) func() bool {
		return func() bool {
			for _, m := range rec.Messages(who + "@example.com") {
				if strings.Contains(m.Body, contains) {
					return true
				}
			}
			return false
		}
	}

	status, issue := srv.post(t, issuesAPI("owner/app"), f.reporter.session, map[string]any{"title": "Crash", "body": "cc @maint and @outer"})
	require.Equal(t, http.StatusOK, status)
	n := issue["number"]
	assert.Eventually(t, got("maint", fmt.Sprintf("reporter opened issue #%v", n)), 2*time.Second, 20*time.Millisecond, "mentioned")
	srv.request(t, http.MethodPut, issuesAPI("owner/app", n, "assignees"), f.maint.session, map[string]any{"assignees": []string{"dev"}})
	assert.Eventually(t, got("dev", "maint assigned dev to issue"), 2*time.Second, 20*time.Millisecond, "the new assignee")
	assert.Eventually(t, got("reporter", "maint assigned dev to issue"), 2*time.Second, 20*time.Millisecond, "the author")
	srv.post(t, issuesAPI("owner/app", n, "comments"), f.dev.session, map[string]any{"body": "Looking"})
	assert.Eventually(t, got("reporter", "dev commented on issue"), 2*time.Second, 20*time.Millisecond)
	assert.Eventually(t, got("reporter", fmt.Sprintf("https://git.example.com/owner/app/issues/%v", n)), 2*time.Second, 20*time.Millisecond, "with a link")

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, rec.Messages("outer@example.com"), "can't read the private repository")
	assert.False(t, got("dev", "dev commented")(), "not about your own activity")
	msgs := rec.Messages("reporter@example.com")
	require.NotEmpty(t, msgs)
	assert.Equal(t, fmt.Sprintf("[owner/app] Crash (#%v)", n), msgs[0].Subject)
}
