package server

import (
	"net/http"
	"os"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryLifecyclePermissions(t *testing.T) {
	srv := startTestServer(t)
	owner, maint, dev, outsider := srv.newUser(t, "owner"), srv.newUser(t, "maint"), srv.newUser(t, "dev"), srv.newUser(t, "outsider")
	srv.createRepo(t, owner.session, "Proj", true)
	for name, role := range map[string]string{"maint": "maintainer", "dev": "developer"} {
		status, _ := srv.request(t, http.MethodPost, "/api/v1/repository/proj/members", owner.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status)
	}
	patch := func(as user, body map[string]any) int {
		status, _ := srv.request(t, http.MethodPatch, "/api/v1/repository/proj", as.session, body)
		return status
	}

	assert.Equal(t, http.StatusOK, patch(maint, map[string]any{"description": "by maint"}), "maintainers edit the description")
	assert.Equal(t, http.StatusForbidden, patch(dev, map[string]any{"description": "x"}), "developers cannot")
	assert.Equal(t, http.StatusNotFound, patch(outsider, map[string]any{"description": "x"}), "non-members do not see the repo")
	for _, body := range []map[string]any{{"name": "Renamed"}, {"is_private": false}, {"archived": true}} {
		assert.Equal(t, http.StatusForbidden, patch(maint, body), "owner only: %v", body)
	}
	status, _ := srv.request(t, http.MethodDelete, "/api/v1/repository/proj?confirm=proj", maint.session, nil)
	assert.Equal(t, http.StatusForbidden, status, "only owners delete")

	status, body := srv.get(t, "/api/v1/repository/proj", owner.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "by maint", body["description"])
}

func TestRepositoryRename(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Old Name", true)
	srv.createRepo(t, owner.session, "Taken", true)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)
	dataDir := srv.repoDir(t, "old-name")

	status, body := srv.request(t, http.MethodPatch, "/api/v1/repository/old-name", owner.session, map[string]any{"name": "New Name"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "new-name", body["slug"])
	assert.DirExists(t, dataDir, "renaming does not move the git data")

	t.Run("new_slug_works", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/new-name", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "New Name", body["name"])
		r, err := clone(t, srv.url+"/new-name.git", owner.git)
		require.NoError(t, err)
		assert.NoError(t, commitAndPush(t, r, owner.git))
		g.mustRun(t.TempDir(), "clone", g.url("new-name"), ".")
	})

	t.Run("old_slug_keeps_working", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/old-name", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "new-name", body["slug"], "the API answers with the current slug")
		r, err := clone(t, srv.url+"/old-name.git", owner.git)
		require.NoError(t, err, "git over HTTP")
		assert.NoError(t, commitAndPush(t, r, owner.git))
		dir := t.TempDir()
		g.mustRun(dir, "clone", g.url("old-name"), ".")
		commitFile(t, g, dir, "via-old.txt", "x")
		g.mustRun(dir, "push", "origin", "HEAD")
	})

	t.Run("conflicts", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPatch, "/api/v1/repository/new-name", owner.session, map[string]any{"name": "Taken"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "REPOSITORY_NAME_TAKEN", body["type"])
		status, _ = srv.request(t, http.MethodPatch, "/api/v1/repository/new-name", owner.session, map[string]any{"name": "!!!"})
		assert.Equal(t, http.StatusBadRequest, status, "names must produce a slug")
	})

	t.Run("a_new_repository_takes_over_the_old_slug", func(t *testing.T) {
		srv.createRepo(t, owner.session, "Old Name", false)
		status, body := srv.get(t, "/api/v1/repository/old-name", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "old-name", body["slug"], "the slug now belongs to the new repository")
		status, body = srv.get(t, "/api/v1/repository/new-name", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "New Name", body["name"], "the renamed repository is unaffected")
	})

	t.Run("rename_back", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, "/api/v1/repository/taken", owner.session, map[string]any{"name": "Taken Two"})
		require.Equal(t, http.StatusOK, status)
		status, body := srv.request(t, http.MethodPatch, "/api/v1/repository/taken-two", owner.session, map[string]any{"name": "Taken"})
		require.Equal(t, http.StatusOK, status, "a repository can take back its own former slug: %v", body)
		status, body = srv.get(t, "/api/v1/repository/taken-two", owner.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "taken", body["slug"], "and the other former slug still redirects")

		status, body = srv.request(t, http.MethodPatch, "/api/v1/repository/taken", owner.session, map[string]any{"name": "Taken Three"})
		assert.Equal(t, http.StatusOK, status, "renaming away again works (no stale redirect left behind): %v", body)
	})
}

func TestRepositoryVisibilityAndArchive(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Proj", true)
	port := srv.startSSH(t)
	key := newSSHKey(t, "owner@laptop")
	status, _ := srv.post(t, "/api/v1/user/ssh-keys", owner.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status)
	g := newGitCLI(t, key, port)
	patch := func(body map[string]any) map[string]any {
		status, resp := srv.request(t, http.MethodPatch, "/api/v1/repository/proj", owner.session, body)
		require.Equal(t, http.StatusOK, status, "%v", resp)
		return resp
	}

	t.Run("visibility", func(t *testing.T) {
		_, err := clone(t, srv.url+"/proj.git", nil)
		require.ErrorIs(t, err, transport.ErrAuthenticationRequired)
		assert.Equal(t, false, patch(map[string]any{"is_private": false})["is_private"])
		_, err = clone(t, srv.url+"/proj.git", nil)
		assert.NoError(t, err, "public: anonymous clone")
		patch(map[string]any{"is_private": true})
		_, err = clone(t, srv.url+"/proj.git", nil)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "private again")
	})

	t.Run("archive", func(t *testing.T) {
		assert.Equal(t, true, patch(map[string]any{"archived": true})["archived"])

		r, err := clone(t, srv.url+"/proj.git", owner.git)
		require.NoError(t, err, "archived repositories can be cloned")
		assert.ErrorIs(t, commitAndPush(t, r, owner.git), transport.ErrAuthorizationFailed, "pushes over HTTP are refused, owners included")

		dir := t.TempDir()
		g.mustRun(dir, "clone", g.url("proj"), ".")
		commitFile(t, g, dir, "x.txt", "x")
		out, err := g.run(dir, "push", "origin", "HEAD")
		assert.Error(t, err)
		assert.Contains(t, out, `repository "proj" is archived (read-only)`, "and over SSH")

		patch(map[string]any{"archived": false})
		g.mustRun(dir, "push", "origin", "HEAD")
	})
}

func TestRepositoryDeleteByOwner(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Doomed", true)
	status, _ := srv.request(t, http.MethodPatch, "/api/v1/repository/doomed", owner.session, map[string]any{"name": "Doomed Two"})
	require.Equal(t, http.StatusOK, status)
	dataDir := srv.repoDir(t, "doomed-two")

	status, _ = srv.request(t, http.MethodDelete, "/api/v1/repository/doomed-two", owner.session, nil)
	assert.Equal(t, http.StatusBadRequest, status, "confirmation required")
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/repository/doomed-two?confirm=doomed", owner.session, nil)
	assert.Equal(t, http.StatusBadRequest, status, "confirm is the current slug")

	status, body := srv.request(t, http.MethodDelete, "/api/v1/repository/doomed-two?confirm=doomed-two", owner.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, err := os.Stat(dataDir)
	assert.True(t, os.IsNotExist(err), "git data removed")
	status, _ = srv.get(t, "/api/v1/repository/doomed", owner.session)
	assert.Equal(t, http.StatusNotFound, status, "the old slug does not point anywhere")
	assert.EqualValues(t, 0, srv.countRows(t, &domain.RepositoryRedirect{}, "slug = ?", "doomed"), "its redirects are removed")

	srv.createRepo(t, owner.session, "Doomed", true) // the slug is free again
}

func TestPublicRepositoryListing(t *testing.T) {
	srv := startTestServer(t)
	owner, other := srv.newUser(t, "owner"), srv.newUser(t, "other")
	srv.createRepo(t, owner.session, "Alpha Tools", false)
	srv.createRepo(t, owner.session, "Beta", false)
	srv.createRepo(t, owner.session, "Hidden", true)
	status, _ := srv.request(t, http.MethodPatch, "/api/v1/repository/beta", owner.session, map[string]any{"description": "contains TOOLS too"})
	require.Equal(t, http.StatusOK, status)

	slugs := func(body map[string]any) []string {
		var out []string
		for _, r := range body["repositories"].([]any) {
			out = append(out, r.(map[string]any)["slug"].(string))
		}
		return out
	}

	status, body := srv.get(t, "/api/v1/repository?scope=public", other.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"alpha-tools", "beta"}, slugs(body), "private repositories are not listed")
	assert.EqualValues(t, 2, body["total"])

	status, body = srv.get(t, "/api/v1/repository?scope=public&q=tools", other.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"alpha-tools", "beta"}, slugs(body), "name or description, case-insensitive")
	status, body = srv.get(t, "/api/v1/repository?scope=public&q=alpha", other.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"alpha-tools"}, slugs(body))
	status, body = srv.get(t, "/api/v1/repository?scope=public&limit=1&offset=1", other.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"beta"}, slugs(body))
	assert.EqualValues(t, 2, body["total"])

	status, _ = srv.get(t, "/api/v1/repository?scope=public", "")
	assert.NotEqual(t, http.StatusOK, status, "logged-in users only")
	status, _ = srv.get(t, "/api/v1/repository?scope=everything", other.session)
	assert.Equal(t, http.StatusBadRequest, status)
	status, body = srv.get(t, "/api/v1/repository", other.session)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, body["repositories"], "the default scope is still 'mine'")
}
