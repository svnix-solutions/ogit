package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenHeader is how CI authenticates: GitHub's "token <pat>".
func tokenHeader(pat string) map[string]string {
	return map[string]string{"Authorization": "token " + pat}
}

// raw sends a JSON request and decodes any JSON answer (objects or arrays).
func (s *testServer) raw(t *testing.T, method, path string, headers map[string]string, body any) (int, any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, s.url+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestCommitStatusAPI(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	sha := pushFiles(t, f.url, f.dev.git, "feature/x", "work", map[string]string{"f.txt": "1\n"})
	statuses := repoAPI("owner/app") + "/statuses/" + sha
	ci := srv.createToken(t, f.dev.session, "ci", "write:status").Password
	post := func(headers map[string]string, body map[string]any) (int, map[string]any) {
		status, resp, _ := srv.do(t, http.MethodPost, statuses, headers, body)
		return status, resp
	}

	t.Run("ci_token_posts", func(t *testing.T) {
		status, body := post(tokenHeader(ci), map[string]any{"state": "pending", "context": "ci/build", "description": "Building", "target_url": "https://ci.example.com/1"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "pending", body["state"])
		assert.Equal(t, "ci/build", body["context"])
		assert.Equal(t, "https://ci.example.com/1", body["target_url"])
		assert.Equal(t, "dev", body["creator"])

		status, body = post(map[string]string{"Authorization": "Bearer " + ci}, map[string]any{"state": "success"})
		require.Equal(t, http.StatusOK, status, "Bearer ogit_... works too: %v", body)
		assert.Equal(t, "default", body["context"], "the default context")
		basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("anything:"+ci))
		status, _ = post(map[string]string{"Authorization": basic}, map[string]any{"state": "success", "context": "basic"})
		assert.Equal(t, http.StatusOK, status, "and Basic with the token as the password")
	})

	t.Run("status_tokens_cannot_use_git", func(t *testing.T) {
		auth := *f.dev.git
		auth.Username, auth.Password = "ci", ci
		_, err := clone(t, f.url, &auth)
		assert.Error(t, err)
	})

	t.Run("who_may_post", func(t *testing.T) {
		status, body := post(map[string]string{"Authorization": "Bearer " + f.reporter.session}, map[string]any{"state": "success"})
		assert.Equal(t, http.StatusForbidden, status, "reporters can't: %v", body)
		reporterCI := srv.createToken(t, f.reporter.session, "reporter-ci", "write:status").Password
		status, _ = post(tokenHeader(reporterCI), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusForbidden, status, "nor their tokens")
		readOnly := srv.createToken(t, f.dev.session, "ro", "read:repository").Password
		status, body = post(tokenHeader(readOnly), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "TOKEN_SCOPE", body["type"])
		pusher := srv.createToken(t, f.dev.session, "pusher", "write:repository").Password
		status, _ = post(tokenHeader(pusher), map[string]any{"state": "success", "context": "push-token"})
		assert.Equal(t, http.StatusOK, status, "write:repository includes statuses")

		outsider := srv.createToken(t, f.outer.session, "outer-ci", "write:status").Password
		status, _ = post(tokenHeader(outsider), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusNotFound, status, "a private repository stays hidden")
		status, _ = post(nil, map[string]any{"state": "success"})
		assert.Equal(t, http.StatusUnauthorized, status)
		status, body = post(tokenHeader("ogit_"+strings.Repeat("0", 64)), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "INVALID_TOKEN", body["type"])
	})

	t.Run("validation", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"state": "done"},
			{"state": "success", "target_url": "javascript:alert(1)"},
			{"state": "success", "target_url": "data:text/html,x"},
			{"state": "success", "target_url": "ftp://ci.example.com/1"},
			{"state": "success", "context": strings.Repeat("c", 256)},
		} {
			status, resp := post(tokenHeader(ci), body)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", body, resp)
		}
		for _, target := range []string{strings.Repeat("0", 40), sha[:12]} {
			status, body, _ := srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+target, tokenHeader(ci), map[string]any{"state": "success"})
			assert.Equal(t, http.StatusUnprocessableEntity, status, "%s: %v", target, body)
		}
		status, body := post(tokenHeader(ci), map[string]any{"state": "success", "context": "long", "description": strings.Repeat("é", 200)})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, 140, len([]rune(body["description"].(string))), "cut to 140 characters")
	})

	t.Run("latest_per_context_and_combined", func(t *testing.T) {
		post(tokenHeader(ci), map[string]any{"state": "failure", "context": "lint"})
		status, body := srv.get(t, repoAPI("owner/app")+"/status?ref=feature/x", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, sha, body["sha"], "by branch name")
		assert.Equal(t, "failure", body["state"])
		post(tokenHeader(ci), map[string]any{"state": "success", "context": "lint"})
		post(tokenHeader(ci), map[string]any{"state": "success", "context": "ci/build"})
		_, body = srv.get(t, repoAPI("owner/app")+"/status?ref="+sha, f.reporter.session)
		assert.Equal(t, "success", body["state"])
		var contexts []string
		for _, s := range body["statuses"].([]any) {
			contexts = append(contexts, s.(map[string]any)["context"].(string))
		}
		assert.Equal(t, []string{"basic", "ci/build", "default", "lint", "long", "push-token"}, contexts, "one per context, sorted")
		assert.EqualValues(t, 6, body["total_count"])

		post(tokenHeader(ci), map[string]any{"state": "pending", "context": "deploy"})
		_, body = srv.get(t, repoAPI("owner/app")+"/status?ref="+sha, f.reporter.session)
		assert.Equal(t, "pending", body["state"])

		_, body = srv.get(t, statuses, f.reporter.session)
		list := body["statuses"].([]any)
		assert.Equal(t, "deploy", list[0].(map[string]any)["context"], "newest first")

		main := tip(t, srv, f.owner, "owner/app", "main")
		_, body = srv.get(t, repoAPI("owner/app")+"/status?ref=main", f.reporter.session)
		assert.Equal(t, main, body["sha"])
		assert.Equal(t, "pending", body["state"], "no statuses yet is pending")
		assert.EqualValues(t, 0, body["total_count"])
		status, _ = srv.get(t, repoAPI("owner/app")+"/status?ref=nope", f.reporter.session)
		assert.Equal(t, http.StatusUnprocessableEntity, status)
	})

	t.Run("batch_and_contexts", func(t *testing.T) {
		main := tip(t, srv, f.owner, "owner/app", "main")
		status, body := srv.get(t, repoAPI("owner/app")+"/statuses?shas="+sha+","+main, f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		commits := body["commits"].(map[string]any)
		assert.Len(t, commits, 1, "commits without statuses are left out")
		assert.Equal(t, "pending", commits[sha].(map[string]any)["state"])
		status, _ = srv.get(t, repoAPI("owner/app")+"/statuses?shas="+strings.Repeat("a,", 101), f.reporter.session)
		assert.Equal(t, http.StatusBadRequest, status)

		_, body = srv.get(t, repoAPI("owner/app")+"/status-contexts", f.reporter.session)
		assert.Contains(t, body["contexts"], "ci/build")
		assert.Equal(t, "deploy", body["contexts"].([]any)[0], "most recent first")
	})

	t.Run("disabled_users_tokens_stop_working", func(t *testing.T) {
		admin := srv.bootstrapAdmin(t)
		status, _ := srv.request(t, http.MethodPatch, "/api/v1/admin/users/dev", admin.session, map[string]any{"active": false})
		require.Equal(t, http.StatusOK, status)
		status, body := post(tokenHeader(ci), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/users/dev", admin.session, map[string]any{"active": true})
		require.Equal(t, http.StatusOK, status)
		status, _ = post(tokenHeader(ci), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("archived_repositories_refuse", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": true})
		require.Equal(t, http.StatusOK, status)
		status, _ = post(tokenHeader(ci), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusConflict, status)
		status, _ = srv.get(t, statuses, f.reporter.session)
		assert.Equal(t, http.StatusOK, status, "still readable")
	})
}

func TestCommitStatusTokenGuessingIsLimited(t *testing.T) {
	srv := startTestServer(t, withLimits)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	path := repoAPI("alice/site") + "/statuses/" + strings.Repeat("a", 40)
	for range 3 {
		status, _, _ := srv.do(t, http.MethodPost, path, tokenHeader("ogit_guess"), map[string]any{"state": "success"})
		require.Equal(t, http.StatusUnauthorized, status)
	}
	status, body, header := srv.do(t, http.MethodPost, path, tokenHeader("ogit_guess"), map[string]any{"state": "success"})
	assert.Equal(t, http.StatusTooManyRequests, status, "%v", body)
	assert.NotEmpty(t, header.Get("Retry-After"))
}

func TestGitHubCompatibleStatuses(t *testing.T) {
	f := newPullFixture(t, true, withPublicURL)
	srv := f.srv
	sha := pushFiles(t, f.url, f.dev.git, "feature/x", "work", nil)
	ci := srv.createToken(t, f.dev.session, "ci", "write:status").Password
	v3 := "/api/v3/repos/owner/app"

	status, out := srv.raw(t, http.MethodPost, v3+"/statuses/"+sha, tokenHeader(ci), map[string]any{
		"state": "success", "context": "ci/build", "description": "All green", "target_url": "https://ci.example.com/7"})
	require.Equal(t, http.StatusCreated, status, "%v", out)
	s := out.(map[string]any)
	assert.Equal(t, "success", s["state"])
	assert.Equal(t, "All green", s["description"])
	assert.Equal(t, "dev", s["creator"].(map[string]any)["login"])
	assert.Equal(t, "https://git.example.com/api/v3/repos/owner/app/statuses/"+sha, s["url"])

	status, out = srv.raw(t, http.MethodPost, v3+"/statuses/"+sha, tokenHeader(ci), map[string]any{"state": "pending", "context": "lint"})
	require.Equal(t, http.StatusCreated, status)
	assert.Nil(t, out.(map[string]any)["description"], "null when empty, like GitHub")
	assert.Nil(t, out.(map[string]any)["target_url"])

	status, out = srv.raw(t, http.MethodGet, v3+"/statuses/"+sha, tokenHeader(ci), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, out, 2, "an array, newest first")
	assert.Equal(t, "lint", out.([]any)[0].(map[string]any)["context"])

	status, out = srv.raw(t, http.MethodGet, v3+"/commits/feature/x/status", tokenHeader(ci), nil)
	require.Equal(t, http.StatusOK, status, "%v", out)
	c := out.(map[string]any)
	assert.Equal(t, "pending", c["state"])
	assert.Equal(t, sha, c["sha"])
	assert.EqualValues(t, 2, c["total_count"])
	assert.Equal(t, "owner/app", c["repository"].(map[string]any)["full_name"])
	status, out = srv.raw(t, http.MethodGet, v3+"/commits/"+sha+"/statuses", tokenHeader(ci), nil)
	require.Equal(t, http.StatusOK, status)
	assert.Len(t, out, 2)

	status, out = srv.raw(t, http.MethodPost, v3+"/statuses/"+sha, tokenHeader(ci), map[string]any{"state": "bogus"})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "GitHub answers validation errors with 422")
	assert.Contains(t, out.(map[string]any)["message"], "Validation Failed")
	status, out = srv.raw(t, http.MethodPost, v3+"/statuses/"+sha, nil, map[string]any{"state": "success"})
	assert.Equal(t, http.StatusUnauthorized, status)
	status, out = srv.raw(t, http.MethodGet, v3+"/statuses/"+sha, nil, nil)
	assert.Equal(t, http.StatusNotFound, status, "private: not found without a token")
	assert.Equal(t, "Not Found", out.(map[string]any)["message"])
	status, out = srv.raw(t, http.MethodGet, v3+"/statuses/"+sha, tokenHeader("ogit_nope"), nil)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "Bad credentials", out.(map[string]any)["message"])

	// The web UI's cookie is not a credential here.
	status, _ = srv.raw(t, http.MethodPost, v3+"/statuses/"+sha, map[string]string{"Cookie": "ogit_session=" + f.dev.session}, map[string]any{"state": "success"})
	assert.Equal(t, http.StatusUnauthorized, status)
}

func TestRequiredStatusChecks(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	rule := srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "main", "required_contexts": []string{"ci/build", "lint", "lint"}})
	assert.Equal(t, []any{"ci/build", "lint"}, rule["required_contexts"], "once each")
	status, body := srv.post(t, repoAPI("owner/app")+"/branch-protection", f.owner.session, map[string]any{"pattern": "x", "required_contexts": []string{" "}})
	assert.Equal(t, http.StatusBadRequest, status, "%v", body)

	ci := srv.createToken(t, f.dev.session, "ci", "write:status").Password
	status, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	require.Equal(t, http.StatusOK, status, "%v", pr)
	n := pr["number"]
	head := tip(t, srv, f.owner, "owner/app", "feature")
	_, list := srv.get(t, pullsAPI("owner/app"), f.reporter.session)
	assert.Equal(t, head, list["pull_requests"].([]any)[0].(map[string]any)["head_commit"], "lists give open pull requests' heads, for status icons")
	report := func(sha, ctx, state string) {
		status, body, _ := srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+sha, tokenHeader(ci), map[string]any{"state": state, "context": ctx})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	view := func() map[string]any {
		_, body := srv.get(t, pullsAPI("owner/app", n), f.maint.session)
		return body
	}
	checks := func(v map[string]any) map[string]string {
		out := map[string]string{}
		for _, c := range v["required_checks"].([]any) {
			out[c.(map[string]any)["context"].(string)] = c.(map[string]any)["state"].(string)
		}
		return out
	}

	v := view()
	assert.Equal(t, map[string]string{"ci/build": "", "lint": ""}, checks(v))
	assert.Equal(t, false, v["can_merge"])
	assert.Equal(t, "CHECKS_REQUIRED", v["merge_blocked"].(map[string]any)["type"])
	assert.Contains(t, v["merge_blocked"].(map[string]any)["message"], "ci/build (not reported), lint (not reported)")

	report(head, "ci/build", "success")
	report(head, "lint", "pending")
	report(head, "other", "failure") // not required: doesn't matter
	v = view()
	assert.Equal(t, map[string]string{"ci/build": "success", "lint": "pending"}, checks(v))
	assert.Contains(t, v["merge_blocked"].(map[string]any)["message"], "lint (pending)")
	status, body = srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "merge"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "CHECKS_REQUIRED", body["type"])

	report(head, "lint", "success")
	assert.Equal(t, true, view()["can_merge"])

	// A new push needs its own checks.
	next := pushOnto(t, f.url, f.dev.git, "feature", "more", map[string]string{"f.txt": "2\n"})
	assert.Equal(t, false, view()["can_merge"])
	report(next, "ci/build", "failure")
	assert.Contains(t, view()["merge_blocked"].(map[string]any)["message"], "ci/build (failure)")

	status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/branch-protection/"+rule["id"].(string), f.owner.session, map[string]any{"owners_bypass": true})
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, view()["can_merge"], "maintainers don't bypass")
	status, body = srv.post(t, pullsAPI("owner/app", n, "merge"), f.owner.session, map[string]any{"method": "merge"})
	assert.Equal(t, http.StatusOK, status, "owners may: %v", body)
}

func TestStatusWebhook(t *testing.T) {
	f := newPullFixture(t, true, allowLoopback, withPublicURL)
	srv := f.srv
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"status"}})
	srv.deliver(t)
	rcv.take()
	sha := pushFiles(t, f.url, f.dev.git, "feature", "work", nil)
	srv.deliver(t)
	require.Empty(t, rcv.take(), "pushes are not status events")

	ci := srv.createToken(t, f.dev.session, "ci", "write:status").Password
	status, body, _ := srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+sha, tokenHeader(ci),
		map[string]any{"state": "failure", "context": "ci/build", "target_url": "https://ci.example.com/9"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.deliver(t)
	got := rcv.take()
	require.Equal(t, []string{"status"}, events(got))
	p := got[0].payload
	assert.Equal(t, sha, p["sha"])
	assert.Equal(t, "failure", p["state"])
	assert.Equal(t, "ci/build", p["context"])
	assert.Equal(t, "https://ci.example.com/9", p["target_url"])
	assert.Nil(t, p["description"])
	assert.Equal(t, "owner/app", p["name"])
	assert.Equal(t, "work", p["commit"].(map[string]any)["commit"].(map[string]any)["message"])
	assert.Equal(t, []any{map[string]any{"name": "feature", "commit": map[string]any{"sha": sha}}}, p["branches"])
	assert.Equal(t, "dev", p["sender"].(map[string]any)["login"])
}
