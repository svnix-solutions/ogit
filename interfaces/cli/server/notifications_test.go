package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// inbox returns as's notifications (query: "", "all=true", ...), by
// subject title.
func (s *testServer) inbox(t *testing.T, as user, query string) map[string]map[string]any {
	t.Helper()
	status, body := s.get(t, "/api/v1/notifications?"+query, as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	out := map[string]map[string]any{}
	for _, n := range body["notifications"].([]any) {
		m := n.(map[string]any)
		out[m["subject"].(map[string]any)["title"].(string)] = m
	}
	return out
}

func (s *testServer) unread(t *testing.T, as user) float64 {
	t.Helper()
	_, body := s.get(t, "/api/v1/notifications/count", as.session)
	return body["unread"].(float64)
}

func (s *testServer) watch(t *testing.T, as user, repo, level string) {
	t.Helper()
	status, body := s.request(t, http.MethodPut, repoAPI(repo)+"/watch", as.session, map[string]any{"level": level})
	require.Equal(t, http.StatusOK, status, "%v", body)
}

func TestNotifications(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.SMTP.Host = "smtp.test" })
	srv := f.srv
	rec := &mailtest.Recorder{}
	srv.deps.NotificationApp.Mailer = rec
	mailed := func(who, contains string) bool {
		for _, m := range rec.Messages(who + "@example.com") {
			if strings.Contains(m.Subject+m.Body, contains) {
				return true
			}
		}
		return false
	}

	var issue map[string]any
	t.Run("participating_and_watching", func(t *testing.T) {
		var status int
		status, issue = srv.post(t, issuesAPI("owner/app"), f.reporter.session, map[string]any{"title": "Crash on start", "body": "cc @maint and @outer"})
		require.Equal(t, http.StatusOK, status)
		n := srv.inbox(t, f.maint, "")["Crash on start"]
		require.NotNil(t, n, "mentioned")
		assert.Equal(t, "mention", n["reason"])
		assert.Equal(t, true, n["unread"])
		assert.Equal(t, true, n["participating"])
		assert.Equal(t, "owner/app", n["repository"])
		assert.Equal(t, "reporter", n["actor"])
		assert.Equal(t, fmt.Sprintf("reporter opened issue #%v", issue["number"]), n["summary"])
		assert.Equal(t, map[string]any{"type": "issue", "number": issue["number"], "title": "Crash on start", "url": fmt.Sprintf("/owner/app/issues/%v", issue["number"])}, n["subject"])

		o := srv.inbox(t, f.owner, "")["Crash on start"]
		require.NotNil(t, o, "the creator watches the repository: new issues")
		assert.Equal(t, "subscribed", o["reason"])
		assert.Equal(t, false, o["participating"])
		assert.Empty(t, srv.inbox(t, f.owner, "participating=true"), "?participating leaves out watched activity")

		assert.Empty(t, srv.inbox(t, f.dev, ""), "not involved")
		assert.Empty(t, srv.inbox(t, f.reporter, ""), "never the actor")
		assert.Empty(t, srv.inbox(t, f.outer, ""), "a mention can't leak a private repository")
		assert.EqualValues(t, 1, srv.unread(t, f.maint))

		assert.Eventually(t, func() bool { return mailed("maint", "reporter opened issue") }, asyncWait, 20*time.Millisecond, "participating: emailed")
		time.Sleep(100 * time.Millisecond)
		assert.False(t, mailed("owner", "Crash on start"), "watched activity isn't emailed by default")
	})

	t.Run("one_row_per_thread", func(t *testing.T) {
		status, _ := srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.dev.session, map[string]any{"body": "Reproduced."})
		require.Equal(t, http.StatusOK, status)
		_, body := srv.get(t, "/api/v1/notifications", f.maint.session)
		require.Len(t, body["notifications"], 1)
		n := body["notifications"].([]any)[0].(map[string]any)
		assert.Equal(t, fmt.Sprintf("dev commented on issue #%v", issue["number"]), n["summary"])
		assert.Equal(t, "dev", n["actor"])
		assert.Equal(t, "author", srv.inbox(t, f.reporter, "")["Crash on start"]["reason"])
		assert.Equal(t, fmt.Sprintf("reporter opened issue #%v", issue["number"]), srv.inbox(t, f.owner, "")["Crash on start"]["summary"],
			"watching all activity: new threads, not every comment")
	})

	t.Run("read_and_unread", func(t *testing.T) {
		n := srv.inbox(t, f.maint, "")["Crash on start"]
		status, _ := srv.request(t, http.MethodPatch, "/api/v1/notifications/"+n["id"].(string), f.maint.session, map[string]any{"unread": false})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, srv.inbox(t, f.maint, ""), "unread only by default")
		assert.Equal(t, false, srv.inbox(t, f.maint, "all=true")["Crash on start"]["unread"])
		assert.EqualValues(t, 0, srv.unread(t, f.maint))
		status, _ = srv.request(t, http.MethodPatch, "/api/v1/notifications/"+n["id"].(string), f.dev.session, map[string]any{"unread": true})
		assert.Equal(t, http.StatusNotFound, status, "only your own")

		srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.reporter.session, map[string]any{"body": "Any news?"})
		assert.Equal(t, true, srv.inbox(t, f.maint, "")["Crash on start"]["unread"], "a new event makes it unread again")
	})

	t.Run("unsubscribe", func(t *testing.T) {
		n := srv.inbox(t, f.maint, "all=true")["Crash on start"]
		status, _ := srv.post(t, "/api/v1/notifications/"+n["id"].(string)+"/mute", f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.reporter.session, map[string]any{"body": "Ping @maint"})
		assert.Empty(t, srv.inbox(t, f.maint, ""), "unsubscribed, even when mentioned")
		status, _ = srv.request(t, http.MethodDelete, "/api/v1/notifications/"+n["id"].(string)+"/mute", f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.reporter.session, map[string]any{"body": "Ping again @maint"})
		assert.NotNil(t, srv.inbox(t, f.maint, "")["Crash on start"], "subscribed again")

		srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.dev.session, map[string]any{"body": "On it."})
		assert.Equal(t, "author", srv.inbox(t, f.reporter, "all=true")["Crash on start"]["reason"], "the author who also commented: the strongest reason")
	})

	t.Run("watch_levels", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("owner/app")+"/watch", f.dev.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "participating", body["level"])
		status, _ = srv.request(t, http.MethodPut, repoAPI("owner/app")+"/watch", f.dev.session, map[string]any{"level": "everything"})
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.get(t, repoAPI("owner/app")+"/watch", f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)

		srv.watch(t, f.dev, "owner/app", "all")
		srv.watch(t, f.reporter, "owner/app", "releases")
		srv.watch(t, f.maint, "owner/app", "ignore")
		status, _ = srv.post(t, issuesAPI("owner/app"), f.owner.session, map[string]any{"title": "Second", "body": "@maint please look"})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "subscribed", srv.inbox(t, f.dev, "")["Second"]["reason"], "all activity")
		status, _ = srv.post(t, issuesAPI("owner/app"), f.reporter.session, map[string]any{"title": "No mention", "body": "Just a report"})
		require.Equal(t, http.StatusOK, status)
		assert.NotNil(t, srv.inbox(t, f.dev, "")["No mention"], "new issues reach watchers without any mention")
		_, quiet := f.open(t, f.owner, "quiet", map[string]string{"q.txt": "q\n"}, map[string]any{"title": "Quiet PR", "body": "no mention"})
		require.NotNil(t, quiet["number"])
		assert.NotNil(t, srv.inbox(t, f.dev, "")["Quiet PR"], "and new pull requests")
		assert.Nil(t, srv.inbox(t, f.reporter, "")["Second"], "releases only")
		assert.Nil(t, srv.inbox(t, f.maint, "")["Second"], "ignore: not even mentions")

		srv.release(t, f.owner, "owner/app", map[string]any{"tag_name": "v1.0.0", "name": "First release"})
		r := srv.inbox(t, f.reporter, "")["First release"]
		require.NotNil(t, r, "releases")
		assert.Equal(t, "release", r["subject"].(map[string]any)["type"])
		assert.Equal(t, "/owner/app/releases/tag/v1.0.0", r["subject"].(map[string]any)["url"])
		assert.Equal(t, "owner published release v1.0.0", r["summary"])
		assert.NotNil(t, srv.inbox(t, f.dev, "")["First release"])
		assert.Nil(t, srv.inbox(t, f.maint, "")["First release"])
		srv.watch(t, f.maint, "owner/app", "participating")
	})

	t.Run("pull_requests", func(t *testing.T) {
		_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, map[string]any{"title": "Add F"})
		status, _ := srv.request(t, http.MethodPut, pullsAPI("owner/app", pr["number"], "reviewers"), f.dev.session, map[string]any{"reviewers": []string{"maint"}})
		require.Equal(t, http.StatusOK, status)
		n := srv.inbox(t, f.maint, "")["Add F"]
		require.NotNil(t, n)
		assert.Equal(t, "review_requested", n["reason"])
		assert.Equal(t, fmt.Sprintf("/owner/app/pulls/%v", pr["number"]), n["subject"].(map[string]any)["url"])

		// A failing check on the head notifies the author.
		head := tip(t, srv, f.owner, "owner/app", "feature")
		ci := srv.repoToken(t, f.owner, "owner/app", "ci", "write:status")
		status, _, _ = srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+head, tokenHeader(ci["token"].(string)), map[string]any{"state": "success", "context": "ci/build"})
		require.Equal(t, http.StatusOK, status)
		assert.Nil(t, srv.inbox(t, f.dev, "")["Add F"], "success: nothing")
		other := tip(t, srv, f.owner, "owner/app", "main")
		status, _, _ = srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+other, tokenHeader(ci["token"].(string)), map[string]any{"state": "failure", "context": "ci/build"})
		require.Equal(t, http.StatusOK, status)
		assert.Nil(t, srv.inbox(t, f.dev, "")["Add F"], "a failure elsewhere: not this pull request")
		status, _, _ = srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+head, tokenHeader(ci["token"].(string)), map[string]any{"state": "failure", "context": "ci/build", "description": "2 tests failed"})
		require.Equal(t, http.StatusOK, status)
		d := srv.inbox(t, f.dev, "")["Add F"]
		require.NotNil(t, d)
		assert.Equal(t, "ci_activity", d["reason"])
		assert.Equal(t, fmt.Sprintf("ci/build: failure on pull request #%v (2 tests failed)", pr["number"]), d["summary"])
	})

	t.Run("read_all_and_email_settings", func(t *testing.T) {
		status, _ := srv.post(t, "/api/v1/notifications/read", f.dev.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 0, srv.unread(t, f.dev))

		status, body := srv.get(t, "/api/v1/notifications/settings", f.owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "participating", body["email"])
		status, _ = srv.request(t, http.MethodPut, "/api/v1/notifications/settings", f.owner.session, map[string]any{"email": "sometimes"})
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.request(t, http.MethodPut, "/api/v1/notifications/settings", f.owner.session, map[string]any{"email": "all"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPut, "/api/v1/notifications/settings", f.maint.session, map[string]any{"email": "none"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.post(t, issuesAPI("owner/app"), f.dev.session, map[string]any{"title": "Third", "body": "@maint"})
		require.Equal(t, http.StatusOK, status)
		assert.Eventually(t, func() bool { return mailed("owner", "Third") }, asyncWait, 20*time.Millisecond, "email: all, watched activity too")
		time.Sleep(100 * time.Millisecond)
		assert.False(t, mailed("maint", "Third"), "email: none")
		assert.NotNil(t, srv.inbox(t, f.maint, "")["Third"], "still in the inbox")
	})

	t.Run("deleting_the_issue", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, issuesAPI("owner/app", issue["number"]), f.owner.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Nil(t, srv.inbox(t, f.reporter, "all=true")["Crash on start"])
	})
}
