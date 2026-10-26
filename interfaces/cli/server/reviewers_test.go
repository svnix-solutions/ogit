package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestedReviewers(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback, func(c *config.Config) { c.SMTP.Host = "smtp.test" })
	srv := f.srv
	rec := &mailtest.Recorder{}
	srv.deps.NotificationApp.Mailer = rec
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"pull_request"}})
	srv.deliver(t)
	rcv.take()
	_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	n := pr["number"]
	reviewers := pullsAPI("owner/app", n, "reviewers")
	requested := func() []any {
		_, p := srv.get(t, pullsAPI("owner/app", n), f.dev.session)
		return p["requested_reviewers"].([]any)
	}
	srv.deliver(t)
	rcv.takeEvents()

	t.Run("request", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPut, reviewers, f.dev.session, map[string]any{"reviewers": []string{"maint", "reporter"}})
		require.Equal(t, http.StatusOK, status, "the author requests: %v", body)
		assert.Equal(t, []any{"maint", "reporter"}, requested())

		srv.deliver(t)
		evs := rcv.takeEvents()
		assert.ElementsMatch(t, []string{"pull_request.review_requested", "pull_request.review_requested"}, names(evs))
		got := []string{}
		for _, e := range evs {
			got = append(got, e.payload["requested_reviewer"].(map[string]any)["login"].(string))
		}
		assert.ElementsMatch(t, []string{"maint", "reporter"}, got)

		assert.Eventually(t, func() bool {
			for _, m := range rec.Messages("reporter@example.com") {
				if strings.Contains(m.Body, "dev requested a review from maint, reporter on pull request") {
					return true
				}
			}
			return false
		}, 2*time.Second, 20*time.Millisecond, "requested reviewers are emailed")
	})

	t.Run("who", func(t *testing.T) {
		for _, names := range [][]string{{"outer"}, {"dev"}, {"nobody"}} {
			status, body := srv.request(t, http.MethodPut, reviewers, f.dev.session, map[string]any{"reviewers": names})
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", names, body)
		}
		status, _ := srv.request(t, http.MethodPut, reviewers, f.reporter.session, map[string]any{"reviewers": []string{"maint"}})
		assert.Equal(t, http.StatusForbidden, status, "readers don't request reviews on others' pull requests")
		status, _ = srv.request(t, http.MethodPut, reviewers, f.outer.session, map[string]any{"reviewers": []string{"maint"}})
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, []any{"maint", "reporter"}, requested(), "unchanged")
	})

	t.Run("reviewing_answers_the_request", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", n, "reviews"), f.maint.session, map[string]any{"state": "approved"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, []any{"reporter"}, requested())
	})

	t.Run("remove", func(t *testing.T) {
		srv.deliver(t)
		rcv.takeEvents()
		status, body := srv.request(t, http.MethodPut, reviewers, f.maint.session, map[string]any{"reviewers": []string{}})
		require.Equal(t, http.StatusOK, status, "writers manage them too: %v", body)
		assert.Empty(t, requested())
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request.review_request_removed"}, names(evs))
		assert.Equal(t, "reporter", evs[0].payload["requested_reviewer"].(map[string]any)["login"])
	})

	t.Run("closed", func(t *testing.T) {
		status, _ := srv.post(t, pullsAPI("owner/app", n, "close"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPut, reviewers, f.dev.session, map[string]any{"reviewers": []string{"maint"}})
		assert.Equal(t, http.StatusConflict, status, "only open pull requests")
	})
}
