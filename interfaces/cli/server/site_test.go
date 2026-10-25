package server

import (
	"net/http"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func repoPaths(body map[string]any) []string {
	paths := []string{}
	for _, r := range body["repositories"].([]any) {
		paths = append(paths, r.(map[string]any)["full_path"].(string))
	}
	return paths
}

func TestSitePages(t *testing.T) {
	srv := startTestServer(t)
	alice, bob, carol := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol")
	srv.createRepo(t, alice.session, "Public Tools", false)
	srv.createRepo(t, alice.session, "Secret", true)
	status, _ := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "core", "description": "Core team"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "hidden"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.post(t, "/api/v1/group/core/members", alice.session, map[string]any{"username": "bob", "role": "member"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.createIn(t, alice, "core", "Core Lib", false)
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.createIn(t, alice, "core", "Core Internal", true)
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.createIn(t, alice, "hidden", "Plans", true)
	require.Equal(t, http.StatusOK, status)

	t.Run("instance", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/instance", "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["registration_enabled"])
		assert.Equal(t, false, body["ssh_enabled"])
		assert.NotContains(t, body, "ssh_port")
		assert.EqualValues(t, 100<<20, body["max_asset_size"], "the default")
	})

	t.Run("explore_without_session", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/explore", "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.ElementsMatch(t, []string{"alice/public-tools", "core/core-lib"}, repoPaths(body))
		status, body = srv.get(t, "/api/v1/explore?q=tools", "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []string{"alice/public-tools"}, repoPaths(body))
	})

	t.Run("user_namespace", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/namespace/alice", "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "user", body["type"])
		assert.Equal(t, []string{"alice/public-tools"}, repoPaths(body), "anonymous: public only")
		assert.Equal(t, false, body["can_create"])

		status, body = srv.get(t, "/api/v1/namespace/alice", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.ElementsMatch(t, []string{"alice/public-tools", "alice/secret"}, repoPaths(body))
		assert.Equal(t, true, body["can_create"])

		status, body = srv.get(t, "/api/v1/namespace/alice", bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []string{"alice/public-tools"}, repoPaths(body))
		assert.Equal(t, false, body["can_create"])

		status, body = srv.get(t, "/api/v1/namespace/alice?limit=1&offset=1", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 2, body["total"])
		assert.Len(t, body["repositories"], 1, "paged")

		status, _ = srv.get(t, "/api/v1/namespace/alice", "not-a-token")
		assert.Equal(t, http.StatusOK, status, "a bad token is just anonymous")
	})

	t.Run("group_namespace", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/namespace/core", bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "group", body["type"])
		assert.Equal(t, "Core team", body["description"])
		assert.ElementsMatch(t, []string{"core/core-lib", "core/core-internal"}, repoPaths(body), "members see the group's private repos")
		assert.Equal(t, false, body["can_create"], "members don't create")
		status, body = srv.get(t, "/api/v1/namespace/core", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, body["can_create"])

		status, body = srv.get(t, "/api/v1/namespace/core", carol.session)
		require.Equal(t, http.StatusOK, status, "a group with a public repo is visible")
		assert.Equal(t, []string{"core/core-lib"}, repoPaths(body))

		for _, as := range []string{"", carol.session} {
			status, _ = srv.get(t, "/api/v1/namespace/hidden", as)
			assert.Equal(t, http.StatusNotFound, status, "a group with nothing visible stays hidden")
		}
		status, _ = srv.get(t, "/api/v1/namespace/nobody", "")
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("with_the_web_cookie", func(t *testing.T) {
		b := newBrowser(t, srv)
		b.login("alice@example.com")
		status, body, _ := b.do(http.MethodGet, "/api/v1/namespace/alice", nil, false)
		require.Equal(t, http.StatusOK, status)
		assert.Contains(t, repoPaths(body), "alice/secret")
	})
}

func TestTokenListAndRevoke(t *testing.T) {
	srv := startTestServer(t)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Repo", true)

	status, body := srv.get(t, "/api/v1/user/tokens", alice.session)
	require.Equal(t, http.StatusOK, status)
	tokens := body["tokens"].([]any)
	require.Len(t, tokens, 1, "newUser's git token")
	token := tokens[0].(map[string]any)
	assert.Equal(t, "alice-git", token["name"])
	assert.NotContains(t, token, "token", "the secret is never listed")
	id := token["id"].(string)

	status, _ = srv.request(t, http.MethodDelete, "/api/v1/user/tokens/"+id, bob.session, nil)
	assert.Equal(t, http.StatusNotFound, status, "not someone else's")
	_, err := clone(t, srv.url+"/alice/repo.git", alice.git)
	require.NoError(t, err)

	status, _ = srv.request(t, http.MethodDelete, "/api/v1/user/tokens/"+id, alice.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, err = clone(t, srv.url+"/alice/repo.git", alice.git)
	assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "revoked tokens stop working")
	status, body = srv.get(t, "/api/v1/user/tokens", alice.session)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, body["tokens"])
}
