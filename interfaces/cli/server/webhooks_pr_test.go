package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookEvent is one received event: X-GitHub-Event, its action, the payload.
type hookEvent struct {
	event, action string
	payload       map[string]any
}

func (r *hookReceiver) takeEvents() []hookEvent {
	var out []hookEvent
	for _, req := range r.take() {
		action, _ := req.payload["action"].(string)
		out = append(out, hookEvent{req.header.Get("X-GitHub-Event"), action, req.payload})
	}
	return out
}

// find returns the event named "event.action"; deliveries run in
// parallel, so their order is not fixed.
func find(t *testing.T, evs []hookEvent, name string) hookEvent {
	t.Helper()
	for _, e := range evs {
		if e.event+"."+e.action == name {
			return e
		}
	}
	require.Failf(t, "event not received", "%s in %v", name, names(evs))
	return hookEvent{}
}

func names(evs []hookEvent) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.event+"."+e.action)
	}
	return out
}

func TestWebhookPullRequestEvents(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback, withPublicURL)
	srv := f.srv
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL,
		"events": []string{"pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment"}})
	srv.deliver(t)
	rcv.take() // ping

	// Pushing a new branch is not a pull request event.
	head := pushFiles(t, f.url, f.dev.git, "feature", "work", map[string]string{"f.txt": "1\n"})
	srv.deliver(t)
	require.Empty(t, rcv.take(), "only push hooks hear pushes")

	status, body := srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "Feature", "body": "Adds f", "source": "feature"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	number := body["number"]
	pr := func(evs []hookEvent, i int) map[string]any { return evs[i].payload["pull_request"].(map[string]any) }

	t.Run("opened", func(t *testing.T) {
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request.opened"}, names(evs))
		p := evs[0].payload
		assert.EqualValues(t, number, p["number"])
		got := pr(evs, 0)
		assert.Equal(t, "open", got["state"])
		assert.Equal(t, "Feature", got["title"])
		assert.Equal(t, "Adds f", got["body"])
		assert.Equal(t, false, got["merged"])
		assert.Equal(t, "https://git.example.com/owner/app/pulls/1", got["html_url"])
		assert.Equal(t, "dev", got["user"].(map[string]any)["login"])
		assert.Equal(t, "feature", got["head"].(map[string]any)["ref"])
		assert.Equal(t, head, got["head"].(map[string]any)["sha"])
		assert.Equal(t, "owner:feature", got["head"].(map[string]any)["label"])
		assert.Equal(t, "main", got["base"].(map[string]any)["ref"])
		assert.Len(t, got["base"].(map[string]any)["sha"], 40)
		assert.Equal(t, "owner/app", got["base"].(map[string]any)["repo"].(map[string]any)["full_name"])
		assert.Equal(t, "dev", p["sender"].(map[string]any)["login"])
	})

	t.Run("synchronize", func(t *testing.T) {
		next := pushOnto(t, f.url, f.dev.git, "feature", "more", map[string]string{"f.txt": "2\n"})
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request.synchronize"}, names(evs))
		assert.Equal(t, head, evs[0].payload["before"])
		assert.Equal(t, next, evs[0].payload["after"])
		assert.Equal(t, next, pr(evs, 0)["head"].(map[string]any)["sha"])
		head = next

		pushFiles(t, f.url, f.dev.git, "unrelated", "x", nil)
		pushOnto(t, f.url, f.dev.git, "unrelated", "y", nil)
		srv.deliver(t)
		assert.Empty(t, rcv.take(), "branches without pull requests")
	})

	t.Run("edited_and_draft", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPatch, pullsAPI("owner/app", number), f.dev.session, map[string]any{"title": "Feature 2", "draft": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.ElementsMatch(t, []string{"pull_request.edited", "pull_request.converted_to_draft"}, names(evs))
		edited := find(t, evs, "pull_request.edited").payload
		assert.Equal(t, "Feature", edited["changes"].(map[string]any)["title"].(map[string]any)["from"])
		assert.Equal(t, "Feature 2", edited["pull_request"].(map[string]any)["title"])
		assert.Equal(t, true, find(t, evs, "pull_request.converted_to_draft").payload["pull_request"].(map[string]any)["draft"])

		status, _ = srv.request(t, http.MethodPatch, pullsAPI("owner/app", number), f.dev.session, map[string]any{"draft": false, "title": "Feature 2"})
		require.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		assert.Equal(t, []string{"pull_request.ready_for_review"}, names(rcv.takeEvents()), "an unchanged title is no edit")
	})

	t.Run("labels", func(t *testing.T) {
		for _, name := range []string{"bug", "docs"} {
			status, body := srv.post(t, repoAPI("owner/app")+"/labels", f.owner.session, map[string]any{"name": name, "color": "#ff0000"})
			require.Equal(t, http.StatusOK, status, "%v", body)
		}
		setLabels := func(labels ...string) []hookEvent {
			status, body := srv.request(t, http.MethodPut, pullsAPI("owner/app", number, "labels"), f.dev.session, map[string]any{"labels": labels})
			require.Equal(t, http.StatusOK, status, "%v", body)
			srv.deliver(t)
			return rcv.takeEvents()
		}
		evs := setLabels("bug")
		require.Equal(t, []string{"pull_request.labeled"}, names(evs))
		assert.Equal(t, "bug", evs[0].payload["label"].(map[string]any)["name"])
		assert.Equal(t, "ff0000", evs[0].payload["label"].(map[string]any)["color"], "without #, like GitHub")
		assert.Len(t, pr(evs, 0)["labels"], 1)

		evs = setLabels("docs")
		require.ElementsMatch(t, []string{"pull_request.labeled", "pull_request.unlabeled"}, names(evs))
		for _, e := range evs {
			want := map[string]string{"labeled": "docs", "unlabeled": "bug"}[e.action]
			assert.Equal(t, want, e.payload["label"].(map[string]any)["name"], e.action)
		}
		assert.Empty(t, setLabels("docs"), "no change, no event")
	})

	var commentId string
	t.Run("conversation_comments_are_issue_comments", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", number, "comments"), f.reporter.session, map[string]any{"body": "Looks good"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		commentId = body["id"].(string)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"issue_comment.created"}, names(evs))
		p := evs[0].payload
		assert.Equal(t, "Looks good", p["comment"].(map[string]any)["body"])
		assert.Equal(t, "reporter", p["comment"].(map[string]any)["user"].(map[string]any)["login"])
		issue := p["issue"].(map[string]any)
		assert.EqualValues(t, number, issue["number"])
		assert.Equal(t, "https://git.example.com/owner/app/pulls/1", issue["pull_request"].(map[string]any)["html_url"], "a pull request, like GitHub says")

		status, _ = srv.request(t, http.MethodPatch, pullsAPI("owner/app", number, "comments", commentId), f.reporter.session, map[string]any{"body": "Looks great"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodDelete, pullsAPI("owner/app", number, "comments", commentId), f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		evs = rcv.takeEvents()
		require.ElementsMatch(t, []string{"issue_comment.edited", "issue_comment.deleted"}, names(evs))
		edited := find(t, evs, "issue_comment.edited").payload
		assert.Equal(t, "Looks good", edited["changes"].(map[string]any)["body"].(map[string]any)["from"])
		assert.Equal(t, "Looks great", edited["comment"].(map[string]any)["body"])
		assert.Equal(t, "maint", find(t, evs, "issue_comment.deleted").payload["sender"].(map[string]any)["login"])
	})

	t.Run("inline_comments_are_review_comments", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", number, "comments"), f.reporter.session,
			map[string]any{"body": "Why 2?", "inline": map[string]any{"path": "f.txt", "line": 1, "side": "new"}})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request_review_comment.created"}, names(evs))
		c := evs[0].payload["comment"].(map[string]any)
		assert.Equal(t, "f.txt", c["path"])
		assert.EqualValues(t, 1, c["line"])
		assert.Equal(t, "RIGHT", c["side"])
		assert.Equal(t, head, c["commit_id"])
		assert.Nil(t, c["pull_request_review_id"])
		assert.EqualValues(t, number, pr(evs, 0)["number"])
	})

	t.Run("reviews", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", number, "reviews"), f.maint.session, map[string]any{
			"state": "approved", "body": "Ship it",
			"comments": []map[string]any{{"path": "f.txt", "line": 1, "side": "new", "body": "nice"}},
		})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.ElementsMatch(t, []string{"pull_request_review.submitted", "pull_request_review_comment.created"}, names(evs))
		review := find(t, evs, "pull_request_review.submitted").payload["review"].(map[string]any)
		assert.Equal(t, "approved", review["state"])
		assert.Equal(t, "Ship it", review["body"])
		assert.Equal(t, "maint", review["user"].(map[string]any)["login"])
		assert.Equal(t, head, review["commit_id"])
		assert.Equal(t, review["id"], find(t, evs, "pull_request_review_comment.created").payload["comment"].(map[string]any)["pull_request_review_id"], "the comment belongs to the review")
	})

	t.Run("closed_and_reopened", func(t *testing.T) {
		status, _ := srv.post(t, pullsAPI("owner/app", number, "close"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.post(t, pullsAPI("owner/app", number, "reopen"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.ElementsMatch(t, []string{"pull_request.closed", "pull_request.reopened"}, names(evs))
		closed := find(t, evs, "pull_request.closed").payload["pull_request"].(map[string]any)
		assert.Equal(t, "closed", closed["state"])
		assert.Equal(t, false, closed["merged"])
		assert.NotNil(t, closed["closed_at"])
		assert.Equal(t, "open", find(t, evs, "pull_request.reopened").payload["pull_request"].(map[string]any)["state"])
	})

	t.Run("merged_is_closed_with_merged_true", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", number, "merge"), f.maint.session, map[string]any{"method": "squash"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request.closed"}, names(evs), "the merge push is a push event, not synchronize")
		got := pr(evs, 0)
		assert.Equal(t, true, got["merged"])
		assert.Equal(t, "closed", got["state"])
		assert.Equal(t, body["merge_commit"], got["merge_commit_sha"])
		assert.Equal(t, "maint", got["merged_by"].(map[string]any)["login"])
		assert.NotNil(t, got["merged_at"])
		assert.Equal(t, head, got["head"].(map[string]any)["sha"], "the merged tips")
	})
}

// Pull request events reach only hooks that want them, and never disclose
// anything to hooks of other repositories.
func TestWebhookPullRequestEventsStayInTheirRepository(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback)
	srv := f.srv
	srv.createRepo(t, f.owner.session, "Other", true)
	mine, other := newHookReceiver(t), newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": mine.URL, "events": []string{"push"}})
	srv.createHook(t, f.owner, "owner/other", map[string]any{"url": other.URL, "events": []string{"*"}})
	srv.deliver(t)
	mine.take()
	other.take()

	status, body := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.deliver(t)
	assert.Equal(t, []string{"push"}, events(mine.take()), "the push only: the hook doesn't want pull_request")
	assert.Empty(t, other.take())
}
