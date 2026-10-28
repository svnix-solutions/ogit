package server

import (
	"net/http"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createIn creates a repository in namespace and returns the status and body.
func (s *testServer) createIn(t *testing.T, as user, namespace, name string, private bool) (int, map[string]any) {
	t.Helper()
	return s.post(t, "/api/v1/repository", as.session, map[string]any{"namespace": namespace, "name": name, "is_private": private})
}

func TestNamespaces(t *testing.T) {
	srv := startTestServer(t)
	alice, bob, carol, dave := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol"), srv.newUser(t, "dave")

	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "platform"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	for name, role := range map[string]string{"bob": "maintainer", "carol": "member"} {
		status, body := srv.post(t, "/api/v1/group/platform/members", alice.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}

	t.Run("group_list_has_roles", func(t *testing.T) {
		for as, role := range map[user]string{alice: "owner", bob: "maintainer", carol: "member"} {
			status, body := srv.get(t, "/api/v1/group", as.session)
			require.Equal(t, http.StatusOK, status)
			groups := body["groups"].([]any)
			require.Len(t, groups, 1)
			assert.Equal(t, role, groups[0].(map[string]any)["role"], "%s's role, so the UI knows where they can create", as.name)
		}
	})

	t.Run("personal_namespace", func(t *testing.T) {
		repo := srv.createRepo(t, alice.session, "Site", true)
		assert.Equal(t, "alice", repo["namespace"])
		assert.Equal(t, "alice/site", repo["full_path"])
		status, _ := srv.createIn(t, bob, "", "Site", true)
		assert.Equal(t, http.StatusOK, status, "the same name in another namespace")
		status, body := srv.createIn(t, alice, "alice", "site", true)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "REPOSITORY_NAME_TAKEN", body["type"])

		status, body = srv.createIn(t, bob, "alice", "Mine", true)
		assert.Equal(t, http.StatusForbidden, status, "not in someone else's namespace: %v", body)
		status, body = srv.createIn(t, bob, "nobody-here", "X", true)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "NAMESPACE_NOT_FOUND", body["type"])
		status, _ = srv.createIn(t, alice, "", "!!!", true)
		assert.Equal(t, http.StatusBadRequest, status, "a name without a slug")
	})

	t.Run("group_namespace", func(t *testing.T) {
		status, body := srv.createIn(t, bob, "platform", "Infra", true)
		require.Equal(t, http.StatusOK, status, "maintainers create in the group: %v", body)
		assert.Equal(t, "platform/infra", body["full_path"])

		status, _ = srv.createIn(t, carol, "platform", "Other", true)
		assert.Equal(t, http.StatusForbidden, status, "members don't")
		status, body = srv.createIn(t, dave, "platform", "Other", true)
		assert.Equal(t, http.StatusNotFound, status, "non-members don't learn the group exists")
		assert.Equal(t, "NAMESPACE_NOT_FOUND", body["type"])
	})

	t.Run("group_roles_apply_to_its_repositories", func(t *testing.T) {
		infra := srv.url + "/platform/infra.git"
		r, err := clone(t, infra, carol.git)
		require.NoError(t, err, "members read")
		require.NoError(t, commitAndPush(t, r, carol.git), "and push (developer)")
		_, err = clone(t, infra, dave.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound, "outsiders see nothing")

		for as, role := range map[user]string{carol: "developer", bob: "maintainer", alice: "owner"} {
			status, body := srv.get(t, repoAPI("platform/infra"), as.session)
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, role, body["role"], "%s's effective role, from the group", as.name)
		}

		status, _ := srv.request(t, http.MethodPatch, repoAPI("platform/infra"), carol.session, map[string]any{"description": "x"})
		assert.Equal(t, http.StatusForbidden, status, "members are not maintainers")
		status, _ = srv.request(t, http.MethodPatch, repoAPI("platform/infra"), bob.session, map[string]any{"description": "by bob"})
		assert.Equal(t, http.StatusOK, status, "maintainers edit")
		status, _ = srv.request(t, http.MethodPatch, repoAPI("platform/infra"), bob.session, map[string]any{"is_private": false})
		assert.Equal(t, http.StatusForbidden, status, "but are not owners")
		status, body := srv.request(t, http.MethodPatch, repoAPI("platform/infra"), alice.session, map[string]any{"archived": false})
		assert.Equal(t, http.StatusOK, status, "the group owner owns its repositories: %v", body)

		status, body = srv.get(t, "/api/v1/repository", carol.session)
		require.Equal(t, http.StatusOK, status)
		var paths []string
		for _, r := range body["repositories"].([]any) {
			paths = append(paths, r.(map[string]any)["full_path"].(string))
		}
		assert.Contains(t, paths, "platform/infra", "listed for group members")
	})

	t.Run("names_are_shared_and_checked", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/auth/register", "", map[string]any{"email": "p@example.com", "username": "platform", "password": "Correct-Horse-Battery-9"})
		assert.Equal(t, http.StatusBadRequest, status, "a group has this name: %v", body)
		status, body = srv.post(t, "/api/v1/group", bob.session, map[string]any{"name": "alice"})
		assert.Equal(t, http.StatusConflict, status, "a user has this name")
		assert.Equal(t, "GROUP_NAME_TAKEN", body["type"])
		for _, name := range []string{"settings", "api", "Alice2", "x.git", "a/b", "-x"} {
			status, _ := srv.post(t, "/api/v1/auth/register", "", map[string]any{"email": "n@example.com", "username": name, "password": "Correct-Horse-Battery-9"})
			assert.Equal(t, http.StatusBadRequest, status, "username %q", name)
			status, _ = srv.post(t, "/api/v1/group", bob.session, map[string]any{"name": name})
			assert.Equal(t, http.StatusBadRequest, status, "group %q", name)
		}
	})

	t.Run("transfer", func(t *testing.T) {
		status, _ := srv.post(t, repoAPI("alice/site")+"/transfer", bob.session, map[string]any{"namespace": "platform"})
		assert.Equal(t, http.StatusNotFound, status, "bob can't see alice's private repo")
		status, _ = srv.post(t, repoAPI("alice/site")+"/transfer", alice.session, map[string]any{"namespace": "bob"})
		assert.Equal(t, http.StatusForbidden, status, "not into someone else's namespace")

		status, body := srv.post(t, repoAPI("alice/site")+"/transfer", alice.session, map[string]any{"namespace": "platform"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "platform/site", body["full_path"])

		status, body = srv.get(t, repoAPI("alice/site"), alice.session)
		require.Equal(t, http.StatusOK, status, "the old path redirects")
		assert.Equal(t, "platform/site", body["full_path"])
		_, err := clone(t, srv.url+"/alice/site.git", alice.git)
		assert.NoError(t, err, "for git too")
		_, err = clone(t, srv.url+"/platform/site.git", carol.git)
		assert.NoError(t, err, "group members now have access")

		status, _ = srv.createIn(t, alice, "", "Site", true)
		assert.Equal(t, http.StatusOK, status, "the old path can be reused")
		status, body = srv.post(t, repoAPI("alice/site")+"/transfer", alice.session, map[string]any{"namespace": "platform"})
		assert.Equal(t, http.StatusConflict, status, "platform already has a site: %v", body)
	})

	t.Run("group_with_repositories_cannot_be_deleted", func(t *testing.T) {
		status, body := srv.request(t, http.MethodDelete, "/api/v1/group/platform", alice.session, nil)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "NAMESPACE_NOT_EMPTY", body["type"])
	})

	t.Run("flat_urls_are_gone", func(t *testing.T) {
		resp, err := http.Get(srv.url + "/site.git/info/refs?service=git-upload-pack")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		_, err = clone(t, srv.url+"/site.git", alice.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound)
		status, _ := srv.get(t, "/api/v1/repository/site", alice.session)
		assert.Equal(t, http.StatusNotFound, status)
	})
}
