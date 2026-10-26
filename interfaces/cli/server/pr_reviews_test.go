package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/labbs/ogit/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRequestReviews(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	dev2 := srv.newUser(t, "dev2")
	status, _ := srv.post(t, repoAPI("owner/app")+"/members", f.owner.session, map[string]any{"username": "dev2", "role": "developer"})
	require.Equal(t, http.StatusOK, status)
	_, pr := f.open(t, f.dev, "feature", map[string]string{"a.txt": "a\n"}, nil)
	n := pr["number"]
	activity := func(as user) map[string]any {
		t.Helper()
		status, body := srv.get(t, pullsAPI("owner/app", n, "activity"), as.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		return body
	}

	t.Run("comments", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", n, "comments"), f.reporter.session, map[string]any{"body": "Looks useful"})
		require.Equal(t, http.StatusOK, status, "readers comment: %v", body)
		commentId := body["id"].(string)
		status, _ = srv.post(t, pullsAPI("owner/app", n, "comments"), f.outer.session, map[string]any{"body": "hi"})
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.post(t, pullsAPI("owner/app", n, "comments"), "", map[string]any{"body": "hi"})
		assert.NotEqual(t, http.StatusOK, status)
		_, body = srv.post(t, pullsAPI("owner/app", n, "comments"), f.reporter.session, map[string]any{"body": " "})
		assert.Equal(t, "EMPTY_COMMENT", body["type"])
		_, body = srv.post(t, pullsAPI("owner/app", n, "comments"), f.reporter.session, map[string]any{"body": "x", "inline": map[string]any{"path": "a.txt", "line": 0, "side": "new"}})
		assert.Equal(t, "INVALID_LINE", body["type"])
		status, _ = srv.post(t, pullsAPI("owner/app", n, "comments"), f.dev.session, map[string]any{"body": "Typo here", "inline": map[string]any{"path": "a.txt", "line": 1, "side": "new"}})
		require.Equal(t, http.StatusOK, status)

		comments := activity(f.reporter)["comments"].([]any)
		require.Len(t, comments, 2)
		inline := comments[1].(map[string]any)
		assert.Equal(t, "a.txt", inline["path"])
		assert.EqualValues(t, 1, inline["line"])
		assert.Equal(t, "new", inline["side"])
		assert.Equal(t, false, inline["can_edit"], "someone else's")
		assert.Equal(t, true, comments[0].(map[string]any)["can_edit"], "own comment")

		status, _ = srv.request(t, http.MethodPatch, pullsAPI("owner/app", n, "comments", commentId), f.dev.session, map[string]any{"body": "edited"})
		assert.Equal(t, http.StatusForbidden, status, "only the author edits")
		status, _ = srv.request(t, http.MethodPatch, pullsAPI("owner/app", n, "comments", commentId), f.reporter.session, map[string]any{"body": "Looks very useful"})
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodDelete, pullsAPI("owner/app", n, "comments", commentId), f.dev.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "developers don't delete others' comments")
		status, _ = srv.request(t, http.MethodDelete, pullsAPI("owner/app", n, "comments", commentId), f.maint.session, nil)
		assert.Equal(t, http.StatusOK, status, "maintainers do")
		assert.Len(t, activity(f.reporter)["comments"], 1)
	})

	t.Run("approvals", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.maint.session, map[string]any{"required_approvals": 2})
		assert.Equal(t, http.StatusForbidden, status, "owners set it: %v", body)
		_, body = srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"required_approvals": 11})
		assert.Equal(t, "INVALID_REQUIRED_APPROVALS", body["type"])
		status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"required_approvals": 2})
		require.Equal(t, http.StatusOK, status)

		review := func(as user, state string) (int, map[string]any) {
			return srv.post(t, pullsAPI("owner/app", n, "reviews"), as.session, map[string]any{"state": state, "body": state})
		}
		status, body = review(f.dev, "approved")
		assert.Equal(t, "OWN_PULL_REQUEST", body["type"], "status %d", status)
		status, _ = review(f.dev, "commented")
		assert.Equal(t, http.StatusOK, status, "authors comment on their own")
		_, body = review(f.reporter, "lgtm")
		assert.Equal(t, "INVALID_REVIEW_STATE", body["type"])
		status, _ = review(f.reporter, "approved")
		require.Equal(t, http.StatusOK, status, "readers may approve")
		status, _ = review(dev2, "approved")
		require.Equal(t, http.StatusOK, status)

		status, body = srv.get(t, pullsAPI("owner/app", n), f.maint.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []any{"dev2"}, body["approved"], "a reporter's approval doesn't count")
		assert.EqualValues(t, 2, body["required_approvals"])
		assert.Equal(t, false, body["can_merge"])
		_, body = srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, "NEEDS_APPROVAL", body["type"])

		status, _ = review(f.maint, "approved")
		require.Equal(t, http.StatusOK, status)
		status, body = review(dev2, "changes_requested")
		require.Equal(t, http.StatusOK, status)
		_, body = srv.get(t, pullsAPI("owner/app", n), f.maint.session)
		assert.Equal(t, []any{"dev2"}, body["changes_requested"], "the latest review counts")
		_, body = srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, "CHANGES_REQUESTED", body["type"])

		status, _ = review(dev2, "approved")
		require.Equal(t, http.StatusOK, status)
		_, body = srv.get(t, pullsAPI("owner/app", n), f.maint.session)
		assert.Equal(t, true, body["can_merge"])
		reviews := activity(f.dev)["reviews"].([]any)
		assert.Len(t, reviews, 6)
		status, body = srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "merge"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = review(dev2, "approved")
		assert.Equal(t, http.StatusConflict, status, "no reviews once merged")
	})
}

func TestPullRequestLabels(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	_, pr := f.open(t, f.dev, "feature", map[string]string{"a.txt": "a\n"}, nil)
	_, other := f.open(t, f.dev, "other", map[string]string{"b.txt": "b\n"}, nil)
	labels := repoAPI("owner/app") + "/labels"

	status, _ := srv.post(t, labels, f.dev.session, map[string]any{"name": "bug", "color": "#d73a4a"})
	assert.Equal(t, http.StatusForbidden, status, "maintainers manage labels")
	_, body := srv.post(t, labels, f.maint.session, map[string]any{"name": "bug", "color": "red"})
	assert.Equal(t, "INVALID_LABEL", body["type"])
	status, bug := srv.post(t, labels, f.maint.session, map[string]any{"name": "bug", "color": "#d73a4a", "description": "Something is broken"})
	require.Equal(t, http.StatusOK, status, "%v", bug)
	_, body = srv.post(t, labels, f.maint.session, map[string]any{"name": "bug", "color": "#000000"})
	assert.Equal(t, "LABEL_EXISTS", body["type"])
	status, _ = srv.post(t, labels, f.maint.session, map[string]any{"name": "docs", "color": "#0075ca"})
	require.Equal(t, http.StatusOK, status)

	status, _ = srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "labels"), f.reporter.session, map[string]any{"labels": []string{"bug"}})
	assert.Equal(t, http.StatusForbidden, status)
	_, body = srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "labels"), f.dev.session, map[string]any{"labels": []string{"nope"}})
	assert.Equal(t, "LABEL_NOT_FOUND", body["type"])
	status, body = srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "labels"), f.dev.session, map[string]any{"labels": []string{"bug", "docs"}})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Len(t, body["labels"], 2)

	status, body = srv.get(t, pullsAPI("owner/app")+"?label=bug", f.reporter.session)
	require.Equal(t, http.StatusOK, status)
	list := body["pull_requests"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, pr["number"], list[0].(map[string]any)["number"])
	assert.Len(t, list[0].(map[string]any)["labels"], 2)
	status, body = srv.get(t, pullsAPI("owner/app")+"?label=none", f.reporter.session)
	require.Equal(t, http.StatusOK, status, "an unknown label lists nothing: %v", body)
	assert.Empty(t, body["pull_requests"])
	_, body = srv.get(t, pullsAPI("owner/app"), f.reporter.session)
	assert.Len(t, body["pull_requests"], 2)
	_ = other

	status, _ = srv.request(t, http.MethodPatch, labels+"/"+bug["id"].(string), f.maint.session, map[string]any{"color": "#ff0000"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.request(t, http.MethodDelete, labels+"/"+bug["id"].(string), f.maint.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, pullsAPI("owner/app", pr["number"]), f.reporter.session)
	assert.Len(t, body["labels"], 1, "a deleted label leaves its pull requests")
}

func TestPullRequestEmails(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.SMTP.Host = "smtp.test"; c.Server.PublicURL = "https://git.example.com" })
	rec := &mailtest.Recorder{}
	f.srv.deps.NotificationApp.Mailer = rec
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

	_, pr := f.open(t, f.dev, "feature", map[string]string{"a.txt": "a\n"}, map[string]any{"body": "cc @maint and @outer."})
	n := pr["number"]
	assert.Eventually(t, got("maint", "dev opened pull request #1"), 2*time.Second, 20*time.Millisecond, "mentioned")
	status, _ := f.srv.post(t, pullsAPI("owner/app", n, "comments"), f.reporter.session, map[string]any{"body": "Nice work"})
	require.Equal(t, http.StatusOK, status)
	assert.Eventually(t, got("dev", "reporter commented"), 2*time.Second, 20*time.Millisecond, "the author")
	assert.Eventually(t, got("dev", "https://git.example.com/owner/app/pulls/1"), 2*time.Second, 20*time.Millisecond, "with a link")
	status, _ = f.srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "squash"})
	require.Equal(t, http.StatusOK, status)
	assert.Eventually(t, got("reporter", "maint merged"), 2*time.Second, 20*time.Millisecond, "commenters")

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, rec.Messages("outer@example.com"), "can't read the private repository")
	assert.False(t, got("reporter", "reporter commented")(), "not about your own activity")
	msgs := rec.Messages("dev@example.com")
	require.NotEmpty(t, msgs)
	assert.Equal(t, "[owner/app] PR from feature (#1)", msgs[0].Subject)
}

func TestPullRequestsSurviveUserAndGoWithRepository(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	_, pr := f.open(t, f.dev, "feature", map[string]string{"a.txt": "a\n"}, nil)
	status, _ := srv.post(t, pullsAPI("owner/app", pr["number"], "comments"), f.dev.session, map[string]any{"body": "note"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": "x", "color": "#000000"})
	require.Equal(t, http.StatusOK, status)

	require.NoError(t, persistence.NewUserPers(srv.deps.Database.Db).Purge(srv.userID(t, "dev")))
	status, body := srv.get(t, pullsAPI("owner/app", pr["number"]), f.owner.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "", body["author"], "kept, by a deleted user")

	status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"?confirm=app", f.owner.session, nil)
	require.Equal(t, http.StatusOK, status)
	for _, model := range []any{&domain.PullRequest{}, &domain.Comment{}, &domain.Label{}} {
		assert.EqualValues(t, 0, srv.countRows(t, model, "1 = 1"), "%T", model)
	}
}

// Services that send mail get the mailer: SSHKeyApp used to be built before
// it existed, so "new SSH key" emails were never sent.
func TestMailerIsWired(t *testing.T) {
	srv := startTestServer(t)
	assert.NotNil(t, srv.deps.SSHKeyApp.Mailer)
	assert.NotNil(t, srv.deps.NotificationApp.Mailer)
	assert.NotNil(t, srv.deps.AuthApp.Mailer)
}
