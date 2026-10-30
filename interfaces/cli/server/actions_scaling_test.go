package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reusableToken makes a reusable registration token at tokenPath.
func (s *testServer) reusableToken(t *testing.T, as user, tokenPath string, opts map[string]any) (id, token string) {
	t.Helper()
	body := map[string]any{"reusable": true}
	for k, v := range opts {
		body[k] = v
	}
	status, resp := s.post(t, tokenPath, as.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	require.Equal(t, true, resp["reusable"])
	return resp["id"].(string), resp["token"].(string)
}

func registerWith(t *testing.T, srv *testServer, token, name string, ephemeral bool) (*actrunner.Client, error) {
	t.Helper()
	reg, err := (&actrunner.Client{URL: srv.url}).Register(context.Background(), token, name, []string{"ubuntu-latest"}, "test", ephemeral)
	if err != nil {
		return nil, err
	}
	return &actrunner.Client{URL: srv.url, Token: reg.Token}, nil
}

// runnersByName lists a repository's runners.
func (s *testServer) runnersByName(t *testing.T, as user, repo string) map[string]map[string]any {
	t.Helper()
	status, body := s.get(t, actionsAPI(repo)+"/runners", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	out := map[string]map[string]any{}
	for _, r := range body["runners"].([]any) {
		out[r.(map[string]any)["name"].(string)] = r.(map[string]any)
	}
	return out
}

func (s *testServer) registrationTokens(t *testing.T, as user, repo string) []map[string]any {
	t.Helper()
	status, body := s.get(t, actionsAPI(repo)+"/runners/registration-tokens", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	out := []map[string]any{}
	for _, tok := range body["tokens"].([]any) {
		out = append(out, tok.(map[string]any))
	}
	return out
}

// queueRuns pushes n commits to main, each queueing a CI run.
func queueRuns(t *testing.T, f *pullFixture, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		files := map[string]string{fmt.Sprintf("n%d.txt", time.Now().UnixNano()): "x\n"}
		if i == 0 && len(f.srv.runs(t, f.owner, "owner/app")) == 0 {
			files[".github/workflows/ci.yml"] = ciWorkflow
		}
		pushFiles(t, f.url, f.owner.git, "main", "run", files)
	}
}

func TestActionsReusableRegistrationTokens(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	tokenPath := actionsAPI("owner/app") + "/runners/registration-token"
	tokensPath := actionsAPI("owner/app") + "/runners/registration-tokens"

	t.Run("one_time_by_default", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPost, tokenPath, f.owner.session, nil)
		require.Equal(t, http.StatusOK, status, "no body: %v", body)
		assert.Equal(t, false, body["reusable"])
		assert.NotNil(t, body["expires_at"])
		_, err := registerWith(t, srv, body["token"].(string), "first", false)
		require.NoError(t, err)
		_, err = registerWith(t, srv, body["token"].(string), "second", false)
		assert.Error(t, err, "works once")
		status, unused := srv.post(t, tokenPath, f.owner.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, srv.registrationTokens(t, f.owner, "owner/app"), "one-time tokens aren't listed")
		status, _ = srv.request(t, http.MethodDelete, tokensPath+"/"+unused["id"].(string), f.owner.session, nil)
		assert.Equal(t, http.StatusNotFound, status, "nor revoked")
	})

	t.Run("invalid", func(t *testing.T) {
		later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
		for name, body := range map[string]map[string]any{
			"expiry_on_a_one_time_token": {"expires_at": later},
			"expiry_in_the_past":         {"reusable": true, "expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)},
			"long_name":                  {"reusable": true, "name": strings.Repeat("x", 101)},
		} {
			status, resp := srv.post(t, tokenPath, f.owner.session, body)
			assert.Equal(t, http.StatusBadRequest, status, "%s: %v", name, resp)
		}
	})

	id, token := srv.reusableToken(t, f.owner, tokenPath, map[string]any{"name": "pool"})

	t.Run("registers_many", func(t *testing.T) {
		for _, name := range []string{"pool-1", "pool-2"} {
			_, err := registerWith(t, srv, token, name, false)
			require.NoError(t, err, name)
		}
		runners := srv.runnersByName(t, f.maint, "owner/app")
		assert.Equal(t, id, runners["pool-1"]["registration_token_id"])
		assert.Nil(t, runners["first"]["registration_token_id"], "one-time tokens aren't recorded")

		tokens := srv.registrationTokens(t, f.maint, "owner/app")
		require.Len(t, tokens, 1)
		assert.Equal(t, id, tokens[0]["id"])
		assert.Equal(t, "pool", tokens[0]["name"])
		assert.Nil(t, tokens[0]["expires_at"], "never expires")
		assert.NotNil(t, tokens[0]["last_used_at"])
		assert.EqualValues(t, 2, tokens[0]["runners"])
		assert.NotContains(t, tokens[0], "token", "the value is shown once")
	})

	t.Run("who", func(t *testing.T) {
		status, _ := srv.post(t, tokenPath, f.dev.session, map[string]any{"reusable": true})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, tokensPath, f.dev.session)
		assert.Equal(t, http.StatusForbidden, status, "developers don't see them")
		status, _ = srv.request(t, http.MethodDelete, tokensPath+"/"+id, f.dev.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		srv.createRepo(t, f.owner.session, "Other", true)
		status, _ = srv.request(t, http.MethodDelete, actionsAPI("owner/other")+"/runners/registration-tokens/"+id, f.owner.session, nil)
		assert.Equal(t, http.StatusNotFound, status, "not through another scope")
	})

	t.Run("expiry", func(t *testing.T) {
		expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		expiring, token := srv.reusableToken(t, f.owner, tokenPath, map[string]any{"expires_at": expires.Format(time.RFC3339)})
		assert.Len(t, srv.registrationTokens(t, f.owner, "owner/app"), 2)
		_, err := registerWith(t, srv, token, "before", false)
		require.NoError(t, err)
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		defer func() { srv.deps.ActionsApp.Now = nil }()
		_, err = registerWith(t, srv, token, "after", false)
		assert.Error(t, err, "expired")
		assert.Len(t, srv.registrationTokens(t, f.owner, "owner/app"), 1, "expired tokens aren't listed")
		srv.deps.ActionsApp.SweepRunners()
		var left int64
		require.NoError(t, srv.deps.Database.Db.Model(&domain.ActionRegistrationToken{}).Where("id IN ?", []string{expiring, id}).Count(&left).Error)
		assert.EqualValues(t, 1, left, "the sweep deletes expired tokens")
	})

	t.Run("revoke", func(t *testing.T) {
		pool, err := registerWith(t, srv, token, "pool-3", false)
		require.NoError(t, err)
		status, body := srv.request(t, http.MethodDelete, tokensPath+"/"+id, f.maint.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, err = registerWith(t, srv, token, "pool-4", false)
		assert.Error(t, err, "revoked")
		assert.Contains(t, srv.runnersByName(t, f.owner, "owner/app"), "pool-3", "its runners stay")
		_, err = pool.Fetch(context.Background(), nil, "test", 0)
		assert.NoError(t, err, "and keep working")
		status, _ = srv.request(t, http.MethodDelete, tokensPath+"/"+id, f.maint.session, nil)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("site", func(t *testing.T) {
		admin := srv.newUser(t, "root")
		srv.promote(t, "root")
		_, token := srv.reusableToken(t, admin, "/api/v1/admin/runners/registration-token", nil)
		_, err := registerWith(t, srv, token, "shared-1", false)
		require.NoError(t, err)
		_, err = registerWith(t, srv, token, "shared-2", false)
		require.NoError(t, err)
		status, body := srv.get(t, "/api/v1/admin/runners/registration-tokens", admin.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		require.Len(t, body["tokens"], 1)
		assert.EqualValues(t, 2, body["tokens"].([]any)[0].(map[string]any)["runners"])
	})
}

func TestActionsOfflineRunnersAreRemoved(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	tokenPath := actionsAPI("owner/app") + "/runners/registration-token"
	_, token := srv.reusableToken(t, f.owner, tokenPath, nil)
	idle, err := registerWith(t, srv, token, "idle", false)
	require.NoError(t, err)
	busy, err := registerWith(t, srv, token, "busy", false)
	require.NoError(t, err)
	srv.registerRunner(t, f.owner, tokenPath, "by-hand", "ubuntu-latest")
	status, body := srv.post(t, tokenPath, f.owner.session, map[string]any{})
	require.Equal(t, http.StatusOK, status)
	_, err = registerWith(t, srv, body["token"].(string), "ephemeral-idle", true)
	require.NoError(t, err)

	queueRuns(t, f, 1)
	task := fetch0(t, busy, "ubuntu-latest")
	require.NotNil(t, task)
	_, err = busy.State(context.Background(), task.RunId, application.StateUpdate{})
	require.NoError(t, err)

	srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(23 * time.Hour) }
	assert.Zero(t, srv.deps.ActionsApp.SweepRunners(), "not a day yet")
	srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	defer func() { srv.deps.ActionsApp.Now = nil }()
	assert.Equal(t, 2, srv.deps.ActionsApp.SweepRunners())

	runners := srv.runnersByName(t, f.owner, "owner/app")
	assert.NotContains(t, runners, "idle", "registered with a reusable token: removed")
	assert.NotContains(t, runners, "ephemeral-idle", "ephemeral: removed")
	assert.Contains(t, runners, "busy", "never while running a run")
	assert.Contains(t, runners, "by-hand", "registered by hand: kept, shown offline")
	_, err = idle.Fetch(context.Background(), nil, "test", 0)
	assert.ErrorIs(t, err, actrunner.ErrUnregistered)
}

func TestActionsEphemeralRunners(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	tokenPath := actionsAPI("owner/app") + "/runners/registration-token"
	_, token := srv.reusableToken(t, f.owner, tokenPath, nil)
	ephemeral := func(name string) *actrunner.Client {
		t.Helper()
		c, err := registerWith(t, srv, token, name, true)
		require.NoError(t, err)
		return c
	}

	t.Run("one_run_then_deleted", func(t *testing.T) {
		c := ephemeral("e1")
		assert.Equal(t, true, srv.runnersByName(t, f.owner, "owner/app")["e1"]["ephemeral"])
		queueRuns(t, f, 2)
		task := fetch0(t, c, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Nil(t, fetch0(t, c, "ubuntu-latest"), "one run only, although another is queued")
		require.NoError(t, c.Log(context.Background(), task.RunId, []byte("done\n")))
		finish(t, c, task, domain.RunSuccess)
		assert.NotContains(t, srv.runnersByName(t, f.owner, "owner/app"), "e1", "deleted when its run ends")
		_, err := c.Fetch(context.Background(), nil, "test", 0)
		assert.ErrorIs(t, err, actrunner.ErrUnregistered)
		// The other run is still there for another runner.
		other := ephemeral("e2")
		next := fetch0(t, other, "ubuntu-latest")
		require.NotNil(t, next)
		finish(t, other, next, domain.RunSuccess)
	})

	t.Run("concurrent_fetches", func(t *testing.T) {
		c := ephemeral("e3")
		queueRuns(t, f, 3)
		var mu sync.Mutex
		var got []*application.Task
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task, err := c.Fetch(context.Background(), []string{"ubuntu-latest"}, "test", 0)
				assert.NoError(t, err)
				if task != nil {
					mu.Lock()
					got = append(got, task)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		require.Len(t, got, 1, "an ephemeral runner takes one run")
		finish(t, c, got[0], domain.RunSuccess)
		drain(t, srv, f)
	})

	t.Run("requeued_run", func(t *testing.T) {
		c := ephemeral("e4")
		queueRuns(t, f, 1)
		require.NotNil(t, fetch0(t, c, "ubuntu-latest"))
		// The answer was lost: the run goes back to the queue, and the runner
		// may take it (or another) again.
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
		require.Equal(t, 1, srv.deps.ActionsApp.Requeue())
		srv.deps.ActionsApp.Now = nil
		task := fetch0(t, c, "ubuntu-latest")
		require.NotNil(t, task, "unspent")
		finish(t, c, task, domain.RunSuccess)
	})

	t.Run("stale_run", func(t *testing.T) {
		c := ephemeral("e5")
		queueRuns(t, f, 1)
		task := fetch0(t, c, "ubuntu-latest")
		require.NotNil(t, task)
		_, err := c.State(context.Background(), task.RunId, application.StateUpdate{})
		require.NoError(t, err)
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
		defer func() { srv.deps.ActionsApp.Now = nil }()
		require.Equal(t, 1, srv.deps.ActionsApp.FailStale())
		assert.Equal(t, 1, srv.deps.ActionsApp.SweepRunners(), "its run is over")
		assert.NotContains(t, srv.runnersByName(t, f.owner, "owner/app"), "e5")
	})

	t.Run("spent_just_before_its_claim", func(t *testing.T) {
		c := ephemeral("e6")
		queueRuns(t, f, 1)
		// Between SpendRunner and ClaimRun, the sweep leaves it alone.
		orig := srv.deps.ActionsApp.Pers
		swept := -1
		srv.deps.ActionsApp.Pers = sweepBeforeClaim{ActionsPers: orig, sweep: func() { swept = srv.deps.ActionsApp.SweepRunners() }}
		defer func() { srv.deps.ActionsApp.Pers = orig }()
		task := fetch0(t, c, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Zero(t, swept)
		srv.deps.ActionsApp.Pers = orig
		finish(t, c, task, domain.RunSuccess)
	})

	t.Run("lost_claim_unspends", func(t *testing.T) {
		c := ephemeral("e7")
		queueRuns(t, f, 1)
		// Another runner takes the run between the spend and the claim.
		orig := srv.deps.ActionsApp.Pers
		srv.deps.ActionsApp.Pers = &claimFailsOnce{ActionsPers: orig}
		assert.Nil(t, fetch0(t, c, "ubuntu-latest"))
		srv.deps.ActionsApp.Pers = orig
		task := fetch0(t, c, "ubuntu-latest")
		require.NotNil(t, task, "it didn't take a run, so it may still")
		finish(t, c, task, domain.RunSuccess)
	})
}

// drain finishes every queued run with a fresh runner.
func drain(t *testing.T, srv *testServer, f *pullFixture) {
	t.Helper()
	c := srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "drain", "ubuntu-latest")
	for task := fetch0(t, c, "ubuntu-latest"); task != nil; task = fetch0(t, c, "ubuntu-latest") {
		finish(t, c, task, domain.RunSuccess)
	}
}

type sweepBeforeClaim struct {
	domain.ActionsPers
	sweep func()
}

func (p sweepBeforeClaim) ClaimRun(runId, runnerId, tokenHash string, now time.Time) (bool, error) {
	p.sweep()
	return p.ActionsPers.ClaimRun(runId, runnerId, tokenHash, now)
}

type claimFailsOnce struct {
	domain.ActionsPers
	failed bool
}

func (p *claimFailsOnce) ClaimRun(runId, runnerId, tokenHash string, now time.Time) (bool, error) {
	if !p.failed {
		p.failed = true
		return false, nil
	}
	return p.ActionsPers.ClaimRun(runId, runnerId, tokenHash, now)
}
