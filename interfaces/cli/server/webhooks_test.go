package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookReceiver records the webhook requests it gets and answers status.
type hookReceiver struct {
	*httptest.Server
	status atomic.Int32
	mu     sync.Mutex
	got    []hookRequest
}

type hookRequest struct {
	header  http.Header
	body    []byte
	payload map[string]any
}

func newHookReceiver(t *testing.T) *hookReceiver {
	r := &hookReceiver{}
	r.status.Store(http.StatusOK)
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		r.mu.Lock()
		r.got = append(r.got, hookRequest{header: req.Header.Clone(), body: body, payload: payload})
		r.mu.Unlock()
		w.WriteHeader(int(r.status.Load()))
		_, _ = w.Write([]byte("thanks"))
	}))
	t.Cleanup(r.Close)
	return r
}

// take returns the requests received since the last call.
func (r *hookReceiver) take() []hookRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	got := r.got
	r.got = nil
	return got
}

// events returns the X-GitHub-Event of each request.
func events(reqs []hookRequest) []string {
	var out []string
	for _, r := range reqs {
		out = append(out, r.header.Get("X-GitHub-Event"))
	}
	return out
}

// pushOnto commits files on top of an existing branch and pushes it.
func pushOnto(t *testing.T, url string, auth *githttp.BasicAuth, branch, msg string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: url, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true})
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		_, err := wt.Add(name)
		require.NoError(t, err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{AllowEmptyCommits: true, Author: &object.Signature{Name: "Dev", Email: "dev@example.com", When: time.Now()}})
	require.NoError(t, err)
	require.NoError(t, repo.Push(&git.PushOptions{Auth: auth}))
	return h.String()
}

// allowLoopback lets webhooks reach the test receivers on 127.0.0.1.
func allowLoopback(c *config.Config) { c.Webhooks.AllowedHosts = []string{"127.0.0.1"} }

func withPublicURL(c *config.Config) { c.Server.PublicURL = "https://git.example.com" }

// deliver sends everything that is due, as the worker would.
func (s *testServer) deliver(t *testing.T) {
	t.Helper()
	for s.deps.WebhookApp.DeliverDue(context.Background()) > 0 {
	}
}

func (s *testServer) createHook(t *testing.T, as user, repo string, body map[string]any) map[string]any {
	t.Helper()
	status, resp := s.post(t, repoAPI(repo)+"/hooks", as.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	return resp
}

func (s *testServer) deliveries(t *testing.T, as user, repo, hookId string) []map[string]any {
	t.Helper()
	status, body := s.get(t, repoAPI(repo)+"/hooks/"+hookId+"/deliveries", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	var out []map[string]any
	for _, d := range body["deliveries"].([]any) {
		out = append(out, d.(map[string]any))
	}
	return out
}

// makeDue moves every pending delivery's next attempt to now.
func (s *testServer) makeDue(t *testing.T) {
	t.Helper()
	require.NoError(t, s.deps.Database.Db.Model(&domain.WebhookDelivery{}).Where("status = ?", domain.DeliveryPending).
		Update("next_attempt_at", time.Now().Add(-time.Second)).Error)
}

func TestWebhookManagement(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback)
	srv := f.srv
	rcv := newHookReceiver(t)
	hooks := repoAPI("owner/app") + "/hooks"

	t.Run("maintainers_and_owners_only", func(t *testing.T) {
		for _, as := range []user{f.owner, f.maint} {
			status, body := srv.get(t, hooks, as.session)
			assert.Equal(t, http.StatusOK, status, "%s: %v", as.name, body)
		}
		for _, as := range []user{f.dev, f.reporter} {
			status, _ := srv.get(t, hooks, as.session)
			assert.Equal(t, http.StatusForbidden, status, as.name)
			status, _ = srv.post(t, hooks, as.session, map[string]any{"url": rcv.URL})
			assert.Equal(t, http.StatusForbidden, status, as.name)
		}
		status, _ := srv.get(t, hooks, f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "a private repository stays hidden")
	})

	t.Run("validation", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"url": "ftp://example.com/hook"},
			{"url": "https://user:pass@example.com/hook"},
			{"url": "not a url"},
			{"url": rcv.URL, "events": []string{"push", "nope"}},
			{"url": rcv.URL, "events": []string{}},
		} {
			status, resp := srv.post(t, hooks, f.owner.session, body)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", body, resp)
			assert.Equal(t, "INVALID_WEBHOOK", resp["type"], "%v", body)
		}
	})

	t.Run("crud", func(t *testing.T) {
		h := srv.createHook(t, f.maint, "owner/app", map[string]any{"url": rcv.URL, "secret": "s3cret"})
		id := h["id"].(string)
		assert.Equal(t, []any{"push"}, h["events"], "push by default")
		assert.Equal(t, true, h["active"])
		assert.Equal(t, true, h["has_secret"])
		assert.NotContains(t, h, "secret", "secrets are write-only")

		status, body := srv.request(t, http.MethodPatch, hooks+"/"+id, f.owner.session, map[string]any{"events": []string{"*"}, "secret": ""})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, []any{"*"}, body["events"])
		assert.Equal(t, false, body["has_secret"])
		assert.Equal(t, rcv.URL, body["url"], "kept")

		status, body = srv.get(t, hooks+"/"+id, f.owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []any{"*"}, body["events"])

		// A hook is only reachable through its own repository.
		srv.createRepo(t, f.owner.session, "Other", true)
		status, _ = srv.get(t, repoAPI("owner/other")+"/hooks/"+id, f.owner.session)
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/other")+"/hooks/"+id, f.owner.session, nil)
		assert.Equal(t, http.StatusNotFound, status)

		status, _ = srv.request(t, http.MethodDelete, hooks+"/"+id, f.dev.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, hooks+"/"+id, f.owner.session, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, hooks+"/"+id, f.owner.session)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Zero(t, srv.countRows(t, &domain.WebhookDelivery{}, "webhook_id = ?", id), "its log is deleted too")
	})

	t.Run("deleted_with_the_repository", func(t *testing.T) {
		srv.createRepo(t, f.owner.session, "Doomed", true)
		h := srv.createHook(t, f.owner, "owner/doomed", map[string]any{"url": rcv.URL})
		status, body := srv.request(t, http.MethodDelete, repoAPI("owner/doomed")+"?confirm=doomed", f.owner.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Zero(t, srv.countRows(t, &domain.Webhook{}, "id = ?", h["id"]))
		assert.Zero(t, srv.countRows(t, &domain.WebhookDelivery{}, "webhook_id = ?", h["id"]))
	})
}

func TestWebhookPush(t *testing.T) {
	t.Run("local", func(t *testing.T) { testWebhookPush(t) })
	t.Run("s3_fake", func(t *testing.T) { testWebhookPush(t, withS3(s3test.StartFake(t))) })
}

func testWebhookPush(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, append(opts, allowLoopback, withPublicURL)...)
	srv := f.srv
	rcv := newHookReceiver(t)
	h := srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "secret": "s3cret", "events": []string{"push"}})
	hookId := h["id"].(string)

	srv.deliver(t)
	got := rcv.take()
	require.Equal(t, []string{"ping"}, events(got), "a ping on creation")
	ping := got[0]
	assert.Equal(t, hookId, ping.payload["hook_id"])
	assert.Equal(t, "owner/app", ping.payload["repository"].(map[string]any)["full_name"])

	t.Run("signed_like_github", func(t *testing.T) {
		mac := hmac.New(sha256.New, []byte("s3cret"))
		mac.Write(ping.body)
		assert.Equal(t, "sha256="+hex.EncodeToString(mac.Sum(nil)), ping.header.Get("X-Hub-Signature-256"))
		assert.Equal(t, "application/json", ping.header.Get("Content-Type"))
		assert.Equal(t, hookId, ping.header.Get("X-GitHub-Hook-ID"))
		assert.NotEmpty(t, ping.header.Get("X-GitHub-Delivery"))
		assert.True(t, strings.HasPrefix(ping.header.Get("User-Agent"), "oGit-Hookshot/"))
	})

	var before string
	t.Run("push_to_a_new_branch", func(t *testing.T) {
		sha := pushFiles(t, f.url, f.dev.git, "feature", "add a and b", map[string]string{"a.txt": "a\n", "dir/b.txt": "b\n"})
		srv.deliver(t)
		got := rcv.take()
		require.Equal(t, []string{"push"}, events(got))
		p := got[0].payload
		assert.Equal(t, "refs/heads/feature", p["ref"])
		assert.Equal(t, strings.Repeat("0", 40), p["before"])
		assert.Equal(t, sha, p["after"])
		assert.Equal(t, true, p["created"])
		assert.Equal(t, false, p["deleted"])
		assert.Equal(t, false, p["forced"])
		commits := p["commits"].([]any)
		require.Len(t, commits, 1, "only the new commit, not the branch's history")
		c := commits[0].(map[string]any)
		assert.Equal(t, sha, c["id"])
		assert.Equal(t, "add a and b", c["message"])
		assert.ElementsMatch(t, []any{"a.txt", "dir/b.txt"}, c["added"])
		assert.Equal(t, "https://git.example.com/owner/app/commit/"+sha, c["url"])
		assert.Equal(t, sha, p["head_commit"].(map[string]any)["id"])
		assert.Equal(t, "dev", p["pusher"].(map[string]any)["name"])
		assert.Equal(t, "dev@example.com", p["pusher"].(map[string]any)["email"])
		assert.Equal(t, "dev", p["sender"].(map[string]any)["login"])
		repo := p["repository"].(map[string]any)
		assert.Equal(t, "owner/app", repo["full_name"])
		assert.Equal(t, true, repo["private"])
		assert.Equal(t, "https://git.example.com/owner/app.git", repo["clone_url"])
		assert.Equal(t, "https://git.example.com/owner/app", repo["html_url"])
		before = sha
	})

	t.Run("push_to_an_existing_branch", func(t *testing.T) {
		first := pushOnto(t, f.url, f.dev.git, "feature", "change a", map[string]string{"a.txt": "a2\n"})
		srv.deliver(t)
		got := rcv.take()
		require.Equal(t, []string{"push"}, events(got))
		p := got[0].payload
		assert.Equal(t, before, p["before"])
		assert.Equal(t, false, p["created"])
		assert.Equal(t, false, p["forced"])
		commits := p["commits"].([]any)
		require.Len(t, commits, 1)
		assert.Equal(t, []any{"a.txt"}, commits[0].(map[string]any)["modified"])
		assert.Equal(t, "https://git.example.com/owner/app/compare/"+before[:12]+"..."+first[:12], p["compare"])
	})

	t.Run("merge_and_branch_deletion_are_pushes", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "Feature", "source": "feature"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, pullsAPI("owner/app", body["number"], "merge"), f.maint.session, map[string]any{"method": "merge", "delete_branch": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.deliver(t)
		got := rcv.take()
		require.Equal(t, []string{"push", "push"}, events(got))
		byRef := map[string]map[string]any{}
		for _, r := range got {
			byRef[r.payload["ref"].(string)] = r.payload
		}
		main := byRef["refs/heads/main"]
		require.NotNil(t, main)
		assert.Equal(t, body["merge_commit"], main["after"])
		assert.Equal(t, "maint", main["sender"].(map[string]any)["login"])
		assert.Len(t, main["commits"], 3, "the merge commit and the two feature commits")
		gone := byRef["refs/heads/feature"]
		require.NotNil(t, gone)
		assert.Equal(t, true, gone["deleted"])
		assert.Equal(t, strings.Repeat("0", 40), gone["after"])
		assert.Empty(t, gone["commits"])
	})

	t.Run("only_subscribed_events", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"description": "changed"})
		require.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		assert.Empty(t, rcv.take(), "the hook wants push only")
	})

	t.Run("inactive_hooks_get_nothing", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/hooks/"+hookId, f.owner.session, map[string]any{"active": false})
		require.Equal(t, http.StatusOK, status)
		pushFiles(t, f.url, f.dev.git, "main", "quiet", nil)
		srv.deliver(t)
		assert.Empty(t, rcv.take())
	})

	t.Run("delivery_log", func(t *testing.T) {
		ds := srv.deliveries(t, f.owner, "owner/app", hookId)
		require.Len(t, ds, 5, "ping, two pushes, merge and deletion")
		assert.Equal(t, "push", ds[0]["event"], "newest first")
		assert.Equal(t, "ping", ds[len(ds)-1]["event"])
		for _, d := range ds {
			assert.Equal(t, "delivered", d["status"])
			assert.EqualValues(t, 200, d["response_status"])
			assert.NotContains(t, d, "payload", "only in a delivery's details")
		}
		status, body := srv.get(t, repoAPI("owner/app")+"/hooks/"+hookId+"/deliveries/"+ds[0]["id"].(string), f.owner.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Contains(t, body["payload"], `"ref":"refs/heads/`)
		assert.Equal(t, "thanks", body["response_body"])
		status, _ = srv.get(t, repoAPI("owner/app")+"/hooks/"+hookId+"/deliveries", f.dev.session)
		assert.Equal(t, http.StatusForbidden, status, "developers don't see payloads")
	})
}

func TestWebhookRepositoryEvents(t *testing.T) {
	srv := startTestServer(t, allowLoopback)
	alice := srv.newUser(t, "alice")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "platform"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	repo := srv.createRepo(t, alice.session, "Site", true)
	rcv := newHookReceiver(t)
	srv.createHook(t, alice, "alice/site", map[string]any{"url": rcv.URL, "events": []string{"repository"}})
	srv.deliver(t)
	require.Equal(t, []string{"ping"}, events(rcv.take()), "pings go to every hook")

	actions := func() []string {
		srv.deliver(t)
		var out []string
		for _, r := range rcv.take() {
			require.Equal(t, "repository", r.header.Get("X-GitHub-Event"))
			out = append(out, r.payload["action"].(string))
		}
		return out
	}
	update := func(fields map[string]any) {
		status, body := srv.request(t, http.MethodPatch, repoAPI(repo["id"].(string)), alice.session, fields)
		require.Equal(t, http.StatusOK, status, "%v", body)
	}

	update(map[string]any{"name": "Website"})
	srv.deliver(t)
	got := rcv.take()
	require.Equal(t, []string{"repository"}, events(got))
	assert.Equal(t, "renamed", got[0].payload["action"])
	assert.Equal(t, "site", got[0].payload["changes"].(map[string]any)["repository"].(map[string]any)["name"].(map[string]any)["from"])
	assert.Equal(t, "alice/website", got[0].payload["repository"].(map[string]any)["full_name"])

	update(map[string]any{"is_private": false})
	assert.Equal(t, []string{"publicized"}, actions())
	update(map[string]any{"is_private": false})
	assert.Empty(t, actions(), "nothing changed")
	update(map[string]any{"archived": true})
	assert.Equal(t, []string{"archived"}, actions())
	update(map[string]any{"archived": false, "is_private": true})
	assert.ElementsMatch(t, []string{"unarchived", "privatized"}, actions())
	update(map[string]any{"description": "Our site"})
	assert.Equal(t, []string{"edited"}, actions())

	status, body = srv.post(t, repoAPI("alice/website")+"/transfer", alice.session, map[string]any{"namespace": "platform"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.deliver(t)
	got = rcv.take()
	require.Len(t, got, 1)
	assert.Equal(t, "transferred", got[0].payload["action"])
	assert.Equal(t, "platform/website", got[0].payload["repository"].(map[string]any)["full_name"])
	assert.Equal(t, "Organization", got[0].payload["repository"].(map[string]any)["owner"].(map[string]any)["type"])
	assert.Equal(t, "alice", got[0].payload["changes"].(map[string]any)["owner"].(map[string]any)["from"].(map[string]any)["user"].(map[string]any)["login"])
}

func TestWebhookBlocksPrivateTargets(t *testing.T) {
	srv := startTestServer(t) // no webhooks.allowed_hosts
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	rcv := newHookReceiver(t)
	h := srv.createHook(t, alice, "alice/site", map[string]any{"url": rcv.URL})

	srv.deliver(t)
	assert.Empty(t, rcv.take(), "127.0.0.1 is not reachable by default")
	ds := srv.deliveries(t, alice, "alice/site", h["id"].(string))
	require.Len(t, ds, 1)
	assert.Equal(t, "pending", ds[0]["status"], "retried later")
	assert.EqualValues(t, 1, ds[0]["attempts"])
	assert.Contains(t, ds[0]["error"], "private or local address")
	next, err := time.Parse(time.RFC3339Nano, ds[0]["next_attempt_at"].(string))
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(30*time.Second), next, 5*time.Second)

	// localhost resolves to a loopback address: also refused.
	status, body := srv.request(t, http.MethodPatch, repoAPI("alice/site")+"/hooks/"+h["id"].(string), alice.session,
		map[string]any{"url": strings.Replace(rcv.URL, "127.0.0.1", "localhost", 1)})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.makeDue(t)
	srv.deliver(t)
	assert.Empty(t, rcv.take())
}

func TestWebhookRetriesAndRedelivery(t *testing.T) {
	srv := startTestServer(t, allowLoopback)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	rcv := newHookReceiver(t)
	rcv.status.Store(http.StatusInternalServerError)
	h := srv.createHook(t, alice, "alice/site", map[string]any{"url": rcv.URL})
	hookId := h["id"].(string)

	var waits []time.Duration
	for attempt := 1; attempt <= 5; attempt++ {
		srv.deliver(t)
		require.Len(t, rcv.take(), 1, "attempt %d", attempt)
		ds := srv.deliveries(t, alice, "alice/site", hookId)
		require.Len(t, ds, 1, "retries reuse the delivery")
		assert.EqualValues(t, attempt, ds[0]["attempts"])
		assert.EqualValues(t, 500, ds[0]["response_status"])
		if attempt < 5 {
			require.Equal(t, "pending", ds[0]["status"])
			next, err := time.Parse(time.RFC3339Nano, ds[0]["next_attempt_at"].(string))
			require.NoError(t, err)
			waits = append(waits, time.Until(next).Round(time.Second))
			srv.deliver(t)
			assert.Empty(t, rcv.take(), "not before it is due")
			srv.makeDue(t)
		} else {
			assert.Equal(t, "failed", ds[0]["status"], "five attempts, then failed")
		}
	}
	for i, want := range []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute} {
		assert.InDelta(t, want.Seconds(), waits[i].Seconds(), 5, "wait after attempt %d", i+1)
	}
	srv.makeDue(t)
	srv.deliver(t)
	assert.Empty(t, rcv.take(), "failed deliveries are not retried")

	rcv.status.Store(http.StatusNoContent)
	failed := srv.deliveries(t, alice, "alice/site", hookId)[0]
	status, body := srv.post(t, repoAPI("alice/site")+"/hooks/"+hookId+"/deliveries/"+failed["id"].(string)+"/attempts", alice.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.NotEqual(t, failed["id"], body["id"], "a new delivery")
	srv.deliver(t)
	got := rcv.take()
	require.Len(t, got, 1)
	assert.Equal(t, "ping", got[0].header.Get("X-GitHub-Event"), "the same payload")
	ds := srv.deliveries(t, alice, "alice/site", hookId)
	require.Len(t, ds, 2)
	assert.Equal(t, "delivered", ds[0]["status"])
	assert.Equal(t, "failed", ds[1]["status"], "the old one keeps its result")

	status, _ = srv.post(t, repoAPI("alice/site")+"/hooks/"+hookId+"/pings", alice.session, nil)
	require.Equal(t, http.StatusOK, status)
	srv.deliver(t)
	assert.Equal(t, []string{"ping"}, events(rcv.take()))
}

func TestWebhookDeliveredOnce(t *testing.T) {
	srv := startTestServer(t, allowLoopback)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	rcv := newHookReceiver(t)
	h := srv.createHook(t, alice, "alice/site", map[string]any{"url": rcv.URL})
	for range 29 {
		status, _ := srv.post(t, repoAPI("alice/site")+"/hooks/"+h["id"].(string)+"/pings", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
	}

	// Three servers sharing the database.
	var wg sync.WaitGroup
	for range 3 {
		worker := *srv.deps.WebhookApp
		wg.Add(1)
		go func() {
			defer wg.Done()
			for worker.DeliverDue(context.Background()) > 0 {
			}
		}()
	}
	wg.Wait()

	seen := map[string]int{}
	for _, r := range rcv.take() {
		seen[r.header.Get("X-GitHub-Delivery")]++
	}
	assert.Len(t, seen, 30)
	for id, n := range seen {
		assert.Equal(t, 1, n, "delivery %s sent %d times", id, n)
	}
}

func TestWebhookPushOverSSH(t *testing.T) {
	srv := startTestServer(t, allowLoopback)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	rcv := newHookReceiver(t)
	srv.createHook(t, alice, "alice/site", map[string]any{"url": rcv.URL})
	srv.deliver(t)
	rcv.take()

	port := srv.startSSH(t)
	key := newSSHKey(t, "alice@x")
	status, body := srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)
	dir := t.TempDir()
	g.mustRun(dir, "clone", g.url("alice/site"), ".")
	commitFile(t, g, dir, "x.txt", "x")
	g.mustRun(dir, "push", g.url("alice/site"), "HEAD:main")
	sha := strings.TrimSpace(g.mustRun(dir, "rev-parse", "HEAD"))

	srv.deliver(t)
	got := rcv.take()
	require.Equal(t, []string{"push"}, events(got))
	assert.Equal(t, "refs/heads/main", got[0].payload["ref"])
	assert.Equal(t, sha, got[0].payload["after"])
	assert.Equal(t, "alice", got[0].payload["sender"].(map[string]any)["login"])
	assert.Equal(t, []any{"x.txt"}, got[0].payload["commits"].([]any)[0].(map[string]any)["added"])
}
