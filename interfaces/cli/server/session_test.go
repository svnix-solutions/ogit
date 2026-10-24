package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfile(t *testing.T) {
	srv := startTestServer(t)
	session := srv.registerAndLogin(t, "me@example.com", "me")

	status, body := srv.get(t, "/api/v1/user/profile", session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "me", body["username"])
	assert.Equal(t, "me@example.com", body["email"])
	assert.NotEmpty(t, body["id"])
	assert.NotContains(t, body, "password")

	status, _ = srv.get(t, "/api/v1/user/profile", "")
	assert.NotEqual(t, http.StatusOK, status, "requires a session")
}

func TestLogout(t *testing.T) {
	srv := startTestServer(t)
	first := srv.registerAndLogin(t, "me@example.com", "me")
	status, body := srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "me@example.com", "password": "Correct-Horse-Battery-9"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	second := body["token"].(string)

	status, body = srv.post(t, "/api/v1/auth/logout", first, map[string]any{})
	require.Equal(t, http.StatusOK, status, "%v", body)

	status, _ = srv.get(t, "/api/v1/user/profile", first)
	assert.NotEqual(t, http.StatusOK, status, "the logged-out token no longer works")

	status, _ = srv.get(t, "/api/v1/user/profile", second)
	assert.Equal(t, http.StatusOK, status, "other sessions are not affected")

	status, _ = srv.post(t, "/api/v1/auth/logout", first, map[string]any{})
	assert.NotEqual(t, http.StatusOK, status, "logging out twice is rejected")
}

func TestDeleteExpiredSessions(t *testing.T) {
	srv := startTestServer(t)
	srv.registerAndLogin(t, "me@example.com", "me")

	var user domain.User
	require.NoError(t, srv.deps.Database.Db.Where("email = ?", "me@example.com").First(&user).Error)

	expired := &domain.Session{UserId: user.Id, ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, srv.deps.SessionApp.Create(expired))

	countSessions := func() int64 {
		var n int64
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("user_id = ?", user.Id).Count(&n).Error)
		return n
	}
	require.EqualValues(t, 2, countSessions(), "login session + expired session")

	require.NoError(t, srv.deps.SessionApp.DeleteExpired())

	assert.EqualValues(t, 1, countSessions(), "only the live session remains")
	var gone domain.Session
	err := srv.deps.Database.Db.Where("id = ?", expired.Id).First(&gone).Error
	assert.Error(t, err, "the expired session is deleted")
}
