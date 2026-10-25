package server

import (
	"net/http"
	"testing"
	"time"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *testServer) repoToken(t *testing.T, as user, repo, name string, scopes ...string) map[string]any {
	t.Helper()
	status, body := s.post(t, repoAPI(repo)+"/tokens", as.session, map[string]any{"name": name, "scopes": scopes})
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

func TestRepositoryTokens(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	srv.createRepo(t, f.owner.session, "Other", true)
	pushFiles(t, srv.url+"/owner/other.git", f.owner.git, "main", "o", map[string]string{"o.txt": "o\n"})
	tokens := repoAPI("owner/app") + "/tokens"

	t.Run("maintainers_manage", func(t *testing.T) {
		status, _ := srv.post(t, tokens, f.dev.session, map[string]any{"name": "ci", "scopes": []string{"read:repository"}})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, tokens, f.dev.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, tokens, f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)

		for _, body := range []map[string]any{
			{"name": "", "scopes": []string{"read:repository"}},
			{"name": "x", "scopes": []string{}},
			{"name": "x", "scopes": []string{"admin:repository"}},
			{"name": "x", "scopes": []string{"read:repository"}, "expires_at": time.Now().Add(-time.Hour)},
		} {
			status, resp := srv.post(t, tokens, f.maint.session, body)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", body, resp)
		}

		tok := srv.repoToken(t, f.maint, "owner/app", "ci", "read:repository", "write:status")
		assert.Regexp(t, `^ogit_[0-9a-f]{64}$`, tok["token"], "the value, once")
		status, body := srv.post(t, tokens, f.maint.session, map[string]any{"name": "ci", "scopes": []string{"read:repository"}})
		assert.Equal(t, http.StatusConflict, status, "%v", body)

		_, body = srv.get(t, tokens, f.owner.session)
		list := body["tokens"].([]any)
		require.Len(t, list, 1)
		assert.NotContains(t, list[0], "token", "never shown again")
		assert.Equal(t, []any{"read:repository", "write:status"}, list[0].(map[string]any)["scopes"])
		assert.Nil(t, list[0].(map[string]any)["last_used_at"])
	})

	t.Run("git", func(t *testing.T) {
		reader := srv.repoToken(t, f.maint, "owner/app", "reader", "read:repository")
		auth := &githttp.BasicAuth{Username: "reader", Password: reader["token"].(string)}
		_, err := clone(t, srv.url+"/owner/app.git", auth)
		require.NoError(t, err, "clones its private repository")
		_, err = clone(t, srv.url+"/owner/other.git", auth)
		assert.Error(t, err, "and no other")
		_, err = pushOntoErr(t, srv.url+"/owner/app.git", auth, "main", "nope")
		assert.Error(t, err, "read-only")

		pusher := srv.repoToken(t, f.maint, "owner/app", "pusher", "read:repository", "write:repository")
		pauth := &githttp.BasicAuth{Username: "pusher", Password: pusher["token"].(string)}
		pushOnto(t, srv.url+"/owner/app.git", pauth, "main", "from CI", map[string]string{"ci.txt": "ci\n"})

		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "main", "allowed_users": []string{"owner"}})
		_, err = pushOntoErr(t, srv.url+"/owner/app.git", pauth, "main", "nope")
		require.Error(t, err, "branch protection applies: a token is never on a rule's list")
		assert.Contains(t, err.Error(), "you are not allowed to push to it")

		_, body := srv.get(t, tokens, f.owner.session)
		for _, tk := range body["tokens"].([]any) {
			if tk.(map[string]any)["name"] == "reader" {
				assert.NotNil(t, tk.(map[string]any)["last_used_at"], "last use recorded")
			}
		}
	})

	t.Run("statuses", func(t *testing.T) {
		ci := srv.repoToken(t, f.maint, "owner/app", "statuses", "write:status")
		sha := tip(t, srv, f.owner, "owner/app", "main")
		status, body, _ := srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+sha, tokenHeader(ci["token"].(string)), map[string]any{"state": "success", "context": "ci/build"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Empty(t, body["creator"], "no person")
		status, _, _ = srv.do(t, http.MethodPost, "/api/v3/repos/owner/app/statuses/"+sha, tokenHeader(ci["token"].(string)), map[string]any{"state": "success", "context": "v3"})
		assert.Equal(t, http.StatusCreated, status, "GitHub-compatible path")

		other := tip(t, srv, f.owner, "owner/other", "main")
		status, _, _ = srv.do(t, http.MethodPost, repoAPI("owner/other")+"/statuses/"+other, tokenHeader(ci["token"].(string)), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusNotFound, status, "only its repository")
		reader := srv.repoToken(t, f.maint, "owner/app", "noscope", "read:repository")
		status, body, _ = srv.do(t, http.MethodPost, repoAPI("owner/app")+"/statuses/"+sha, tokenHeader(reader["token"].(string)), map[string]any{"state": "success"})
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
	})

	t.Run("reads_the_api", func(t *testing.T) {
		tok := srv.repoToken(t, f.maint, "owner/app", "api", "read:repository")
		h := tokenHeader(tok["token"].(string))
		bug := srv.openIssue(t, f.reporter, "owner/app", "Bug")
		_, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
		for _, path := range []string{repoAPI("owner/app"), repoAPI("owner/app") + "/refs", issuesAPI("owner/app"), pullsAPI("owner/app"),
			pullsAPI("owner/app", pr["number"]), repoAPI("owner/app") + "/milestones"} {
			status, body, _ := srv.do(t, http.MethodGet, path, h, nil)
			assert.Equal(t, http.StatusOK, status, "%s: %v", path, body)
		}
		_, issue, _ := srv.do(t, http.MethodGet, issuesAPI("owner/app", bug["number"]), h, nil)
		assert.Equal(t, false, issue["can_comment"], "a token is not a person")
		_, p, _ := srv.do(t, http.MethodGet, pullsAPI("owner/app", pr["number"]), h, nil)
		assert.Equal(t, false, p["can_review"])
		_, mine, _ := srv.do(t, http.MethodGet, issuesAPI("owner/app")+"?mine=assigned", h, nil)
		assert.NotEqual(t, http.StatusOK, mine["code"], "no \"mine\" for tokens")

		status, _, _ := srv.do(t, http.MethodGet, repoAPI("owner/other"), h, nil)
		assert.Equal(t, http.StatusNotFound, status, "other private repositories stay hidden")
		status, _, _ = srv.do(t, http.MethodPost, issuesAPI("owner/app"), h, map[string]any{"title": "x"})
		assert.NotEqual(t, http.StatusOK, status, "no writes through the API")

		_, listed := srv.get(t, tokens, f.owner.session)
		for _, tk := range listed["tokens"].([]any) {
			if tk.(map[string]any)["name"] == "api" {
				assert.NotNil(t, tk.(map[string]any)["last_used_at"], "API use recorded too")
			}
		}

		statusOnly := srv.repoToken(t, f.maint, "owner/app", "status-only", "write:status")
		status, _, _ = srv.do(t, http.MethodGet, issuesAPI("owner/app"), tokenHeader(statusOnly["token"].(string)), nil)
		assert.Equal(t, http.StatusNotFound, status, "reading needs read:repository")
	})

	t.Run("revoked_and_expired", func(t *testing.T) {
		tok := srv.repoToken(t, f.maint, "owner/app", "short", "read:repository")
		h := tokenHeader(tok["token"].(string))
		status, _, _ := srv.do(t, http.MethodGet, issuesAPI("owner/app"), h, nil)
		require.Equal(t, http.StatusOK, status)
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Token{}).Where("id = ?", tok["id"]).Update("expires_at", time.Now().Add(-time.Minute)).Error)
		status, _, _ = srv.do(t, http.MethodGet, issuesAPI("owner/app"), h, nil)
		assert.Equal(t, http.StatusNotFound, status, "expired")

		tok = srv.repoToken(t, f.maint, "owner/app", "gone", "read:repository")
		status, _ = srv.request(t, http.MethodDelete, tokens+"/"+tok["id"].(string), f.dev.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, repoAPI("owner/other")+"/tokens/"+tok["id"].(string), f.owner.session, nil)
		assert.Equal(t, http.StatusNotFound, status, "revoked through its own repository only")
		status, _ = srv.request(t, http.MethodDelete, tokens+"/"+tok["id"].(string), f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		_, err := clone(t, srv.url+"/owner/app.git", &githttp.BasicAuth{Username: "gone", Password: tok["token"].(string)})
		assert.Error(t, err)
	})
}

// On Postgres, the token principal must never reach a UUID column.
func TestRepositoryTokenPrincipalNeverQueriesUsers(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "App", false)
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "team"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	tok := srv.repoToken(t, alice, "alice/app", "ci", "read:repository")
	h := tokenHeader(tok["token"].(string))
	for _, path := range []string{repoAPI("alice/app"), repoAPI("alice/app") + "/forks", issuesAPI("alice/app") + "?state=all", "/api/v1/namespace/alice", "/api/v1/explore"} {
		status, body, _ := srv.do(t, http.MethodGet, path, h, nil)
		assert.Equal(t, http.StatusOK, status, "%s: %v", path, body)
	}
	// Groups stay hidden from tokens (no member), but never by an error.
	status, body, _ = srv.do(t, http.MethodGet, "/api/v1/namespace/team", h, nil)
	assert.Equal(t, http.StatusNotFound, status, "%v", body)
}
