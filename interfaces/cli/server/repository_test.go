package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRepository(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.registerAndLogin(t, "owner@example.com", "owner")
	outsider := srv.registerAndLogin(t, "outsider@example.com", "outsider")

	private := srv.createRepo(t, owner, "Private Repo", true)
	srv.createRepo(t, owner, "Public Repo", false)

	t.Run("by_slug", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/private-repo", owner)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Private Repo", body["name"])
		assert.Equal(t, true, body["is_private"])
	})

	t.Run("by_id", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/"+private["id"].(string), owner)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "private-repo", body["slug"])
	})

	t.Run("missing", func(t *testing.T) {
		status, _ := srv.get(t, "/api/v1/repository/does-not-exist", owner)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("private_repo_hidden_from_non_members", func(t *testing.T) {
		status, _ := srv.get(t, "/api/v1/repository/private-repo", outsider)
		assert.Equal(t, http.StatusNotFound, status, "by slug")
		status, _ = srv.get(t, "/api/v1/repository/"+private["id"].(string), outsider)
		assert.Equal(t, http.StatusNotFound, status, "by id")
	})

	t.Run("public_repo_visible_to_others", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/public-repo", outsider)
		assert.Equal(t, http.StatusOK, status, "%v", body)
	})

	t.Run("requires_session", func(t *testing.T) {
		status, _ := srv.get(t, "/api/v1/repository/public-repo", "")
		assert.NotEqual(t, http.StatusOK, status)
	})
}
