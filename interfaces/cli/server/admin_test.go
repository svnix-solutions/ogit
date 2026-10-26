package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// promote makes username a site admin, with two-factor authentication
// (admin actions need it; TestAdminsNeedTwoFactor checks that).
func (s *testServer) promote(t *testing.T, username string) {
	t.Helper()
	require.NoError(t, s.deps.Database.Db.Model(&domain.User{}).Where("username = ?", username).Update("role", domain.RoleAdmin).Error)
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("username = ?", username).First(&u).Error)
	if on, err := s.deps.TwoFactorApp.Enabled(u.Id); err == nil && on {
		return // keep a real authenticator app
	}
	now := time.Now()
	require.NoError(t, s.deps.Database.Db.Save(&domain.UserTOTP{UserId: u.Id, Secret: "not-used", ConfirmedAt: &now}).Error)
}

func TestAdminRequiresAdmin(t *testing.T) {
	srv := startTestServer(t)
	plain := srv.newUser(t, "plain")

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/users"},
		{http.MethodPost, "/api/v1/admin/users"},
		{http.MethodPatch, "/api/v1/admin/users/plain"},
		{http.MethodDelete, "/api/v1/admin/users/plain"},
		{http.MethodGet, "/api/v1/admin/repositories"},
		{http.MethodPost, "/api/v1/admin/repositories/x/owner"},
		{http.MethodDelete, "/api/v1/admin/repositories/x"},
		{http.MethodPost, "/api/v1/admin/groups/x/transfer"},
	} {
		body := map[string]any{"username": "plain", "email": "x@example.com", "password": testPassword, "active": false}
		status, _ := srv.request(t, r.method, r.path, plain.session, body)
		assert.Equal(t, http.StatusForbidden, status, "%s %s", r.method, r.path)
	}

	srv.promote(t, "plain")
	status, _ := srv.get(t, "/api/v1/admin/users", plain.session)
	assert.Equal(t, http.StatusOK, status, "admin status is read on every request")

	require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "plain").Update("role", domain.RoleUser).Error)
	status, _ = srv.get(t, "/api/v1/admin/users", plain.session)
	assert.Equal(t, http.StatusForbidden, status, "demotion takes effect immediately")
}

func TestAdminUsers(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) { c.Registration.Enabled = false })
	// Registration is off: the first accounts are created directly.
	admin := srv.bootstrapAdmin(t)

	t.Run("create", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPost, "/api/v1/admin/users", admin.session,
			map[string]any{"username": "alice", "email": "alice@example.com", "password": testPassword})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "user", body["role"])
		assert.NotNil(t, body["email_verified_at"], "created accounts are verified")
		status, _ = srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusOK, status)

		status, _ = srv.request(t, http.MethodPost, "/api/v1/admin/users", admin.session,
			map[string]any{"username": "weak", "email": "weak@example.com", "password": "short"})
		assert.Equal(t, http.StatusBadRequest, status, "password policy applies")
		status, _ = srv.request(t, http.MethodPost, "/api/v1/admin/users", admin.session,
			map[string]any{"username": "alice", "email": "other@example.com", "password": testPassword})
		assert.Equal(t, http.StatusConflict, status)
	})

	t.Run("list_and_search", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/admin/users", admin.session)
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 2, body["total"])
		status, body = srv.get(t, "/api/v1/admin/users?q=ALICE", admin.session)
		require.Equal(t, http.StatusOK, status)
		require.EqualValues(t, 1, body["total"])
		assert.Equal(t, "alice", body["users"].([]any)[0].(map[string]any)["username"])
		assert.NotContains(t, body["users"].([]any)[0], "password")
		status, body = srv.get(t, "/api/v1/admin/users?limit=1&offset=1", admin.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["users"], 1)
		assert.EqualValues(t, 2, body["total"])
	})

	t.Run("disable_takes_effect_immediately", func(t *testing.T) {
		alice := user{name: "alice"}
		_, body := srv.login(t, "alice@example.com")
		alice.session = body["token"].(string)
		alice.git = srv.createToken(t, alice.session, "alice-git", "read:repository", "write:repository")
		srv.createRepo(t, alice.session, "Alice Repo", true)

		status, _ := srv.request(t, http.MethodPatch, "/api/v1/admin/users/alice", admin.session, map[string]any{"active": false})
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 0, srv.countRows(t, &domain.Session{}, "user_id = ?", srv.userID(t, "alice")), "sessions are deleted")

		status, _ = srv.get(t, "/api/v1/user/profile", alice.session)
		assert.NotEqual(t, http.StatusOK, status, "existing session ends")
		status, body = srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "ACCOUNT_DISABLED", body["type"])
		_, err := clone(t, srv.url+"/alice/alice-repo.git", alice.git)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "git tokens stop working")

		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/users/alice", admin.session, map[string]any{"active": true})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusOK, status)
		_, err = clone(t, srv.url+"/alice/alice-repo.git", alice.git)
		assert.NoError(t, err)
	})

	t.Run("last_admin_is_protected", func(t *testing.T) {
		for _, body := range []map[string]any{{"role": "user"}, {"active": false}} {
			status, resp := srv.request(t, http.MethodPatch, "/api/v1/admin/users/root", admin.session, body)
			assert.Equal(t, http.StatusConflict, status, "%v", body)
			assert.Equal(t, "LAST_ADMIN", resp["type"])
		}
		status, _ := srv.request(t, http.MethodDelete, "/api/v1/admin/users/root", admin.session, nil)
		assert.Equal(t, http.StatusConflict, status)

		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/users/alice", admin.session, map[string]any{"role": "admin"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/users/root", admin.session, map[string]any{"role": "user"})
		assert.Equal(t, http.StatusOK, status, "with a second admin, the first can step down")
		status, _ = srv.get(t, "/api/v1/admin/users", admin.session)
		assert.Equal(t, http.StatusForbidden, status)

		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/users/alice", admin.session, map[string]any{"role": "superuser"})
		assert.Equal(t, http.StatusForbidden, status, "no longer admin")
	})
}

func TestAdminMarksEmailVerified(t *testing.T) {
	srv, _ := startVerifyingServer(t)
	admin := srv.bootstrapAdmin(t)
	srv.register(t, "pending@example.com")

	status, _ := srv.login(t, "pending@example.com")
	require.Equal(t, http.StatusForbidden, status)
	status, body := srv.request(t, http.MethodPatch, "/api/v1/admin/users/pending", admin.session, map[string]any{"email_verified": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.NotNil(t, body["email_verified_at"])
	status, _ = srv.login(t, "pending@example.com")
	assert.Equal(t, http.StatusOK, status)
}

func TestAdminRepositoriesAndDeletion(t *testing.T) {
	srv := startTestServer(t)
	admin := srv.bootstrapAdmin(t)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Secret", true)
	status, _ := srv.request(t, http.MethodPost, "/api/v1/group", alice.session, map[string]any{"name": "alices-team"})
	require.Equal(t, http.StatusOK, status)

	t.Run("no_implicit_access", func(t *testing.T) {
		status, _ := srv.get(t, repoAPI("alice/secret"), admin.session)
		assert.Equal(t, http.StatusNotFound, status, "admins do not see private repos")
		_, err := clone(t, srv.url+"/alice/secret.git", admin.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound)

		status, body := srv.get(t, "/api/v1/admin/repositories", admin.session)
		require.Equal(t, http.StatusOK, status)
		assert.EqualValues(t, 1, body["total"], "but can list every repo")
	})

	t.Run("delete_user_refused_while_owning", func(t *testing.T) {
		status, body := srv.request(t, http.MethodDelete, "/api/v1/admin/users/alice", admin.session, nil)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "WOULD_ORPHAN", body["type"])
		assert.Contains(t, body["details"], "repositories: alice/secret")
		assert.Contains(t, body["details"], "groups: alices-team")
	})

	t.Run("recover_repository_and_group", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPost, "/api/v1/admin/repositories/"+url.PathEscape("alice/secret")+"/owner", admin.session, map[string]any{"username": "bob"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "owner", body["role"])
		assert.Equal(t, "owner", srv.members(t, "alice/secret", bob)["bob"]["role"], "the owner shows up in the member list")
		_, err := clone(t, srv.url+"/alice/secret.git", bob.git)
		assert.NoError(t, err)

		status, body = srv.request(t, http.MethodPost, "/api/v1/admin/groups/alices-team/transfer", admin.session, map[string]any{"username": "bob"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.get(t, "/api/v1/group/alices-team", bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "owner", body["role"])
	})

	t.Run("delete_user", func(t *testing.T) {
		aliceID := srv.userID(t, "alice")
		status, body := srv.request(t, http.MethodDelete, "/api/v1/admin/users/alice", admin.session, nil)
		assert.Equal(t, http.StatusConflict, status, "the repository is still in alice's namespace, even with another owner")
		assert.Contains(t, body["details"], "repositories: alice/secret")

		status, body = srv.request(t, http.MethodPost, "/api/v1/admin/repositories/"+url.PathEscape("alice/secret")+"/transfer", admin.session, map[string]any{"namespace": "bob"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "bob/secret", body["full_path"])

		status, body = srv.request(t, http.MethodDelete, "/api/v1/admin/users/alice", admin.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, err := clone(t, srv.url+"/bob/secret.git", bob.git)
		assert.NoError(t, err, "the repository survives under bob")
		for _, table := range []any{&domain.Session{}, &domain.Token{}, &domain.GroupMember{}, &domain.RepositoryMember{}} {
			assert.EqualValues(t, 0, srv.countRows(t, table, "user_id = ?", aliceID), "%T rows are removed", table)
		}

		status, _ = srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusUnauthorized, status)
		status, _ = srv.get(t, "/api/v1/user/profile", alice.session)
		assert.NotEqual(t, http.StatusOK, status, "sessions are gone")
		_, err = clone(t, srv.url+"/alice/secret.git", alice.git)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "tokens are gone")
		_, ok := srv.members(t, "alice/secret", bob)["alice"]
		assert.False(t, ok, "memberships are gone")
	})

	t.Run("delete_repository", func(t *testing.T) {
		adminRepo := "/api/v1/admin/repositories/" + url.PathEscape("bob/secret")
		repoDir := srv.repoDir(t, "bob/secret")
		require.DirExists(t, repoDir)

		status, _ := srv.request(t, http.MethodDelete, adminRepo, admin.session, nil)
		assert.Equal(t, http.StatusBadRequest, status, "confirmation required")
		status, _ = srv.request(t, http.MethodDelete, adminRepo+"?confirm=other", admin.session, nil)
		assert.Equal(t, http.StatusBadRequest, status)

		status, body := srv.request(t, http.MethodDelete, adminRepo+"?confirm=secret", admin.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.get(t, repoAPI("bob/secret"), bob.session)
		assert.Equal(t, http.StatusNotFound, status)
		_, err := clone(t, srv.url+"/bob/secret.git", bob.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound)
		_, statErr := os.Stat(repoDir)
		assert.True(t, os.IsNotExist(statErr), "git data is removed")

		status, body = srv.get(t, "/api/v1/repository", bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, body["repositories"], "no dangling membership")
	})
}

func TestChangePassword(t *testing.T) {
	srv := startTestServer(t)
	me := srv.newUser(t, "me")
	_, body := srv.login(t, "me@example.com")
	other := body["token"].(string)
	const next = "Brand-New-Passw0rd"

	change := func(current, newPassword string) (int, map[string]any) {
		return srv.post(t, "/api/v1/user/password", me.session, map[string]any{"current_password": current, "new_password": newPassword})
	}

	status, body := change("Wrong-Password-1", next)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "INVALID_PASSWORD", body["type"])
	status, _ = change(testPassword, "weak")
	assert.Equal(t, http.StatusBadRequest, status, "password policy applies")

	status, body = change(testPassword, next)
	require.Equal(t, http.StatusOK, status, "%v", body)

	status, _ = srv.login(t, "me@example.com")
	assert.Equal(t, http.StatusUnauthorized, status, "old password no longer works")
	status, _ = srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "me@example.com", "password": next})
	assert.Equal(t, http.StatusOK, status)
	status, _ = srv.get(t, "/api/v1/user/profile", me.session)
	assert.Equal(t, http.StatusOK, status, "the current session is kept")
	status, _ = srv.get(t, "/api/v1/user/profile", other)
	assert.NotEqual(t, http.StatusOK, status, "other sessions end")
}

// bootstrapAdmin creates the account "root" (directly in the database when
// registration is off) and makes it an admin, as `ogit admin promote` would.
func (s *testServer) bootstrapAdmin(t *testing.T) user {
	t.Helper()
	if s.deps.Config.Registration.Enabled {
		s.register(t, "root@example.com")
	} else {
		hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
		require.NoError(t, err)
		now := time.Now()
		require.NoError(t, s.deps.Database.Db.Create(&domain.User{
			Id: utils.UUIDv4(), Username: "root", Email: "root@example.com", Password: string(hash), Active: true, EmailVerifiedAt: &now,
		}).Error)
	}
	// An operator bootstrapping an instance with verification on verifies too.
	require.NoError(t, s.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "root").Update("email_verified_at", time.Now()).Error)
	status, body := s.login(t, "root@example.com")
	require.Equal(t, http.StatusOK, status, "%v", body)
	session := body["token"].(string)
	s.promote(t, "root") // after signing in: promoting adds 2FA
	return user{name: "root", session: session, git: s.createToken(t, session, "root-git", "read:repository", "write:repository")}
}

// A disabled account cannot use a session, even one that was not deleted
// when it was disabled (e.g. disabled directly in the database).
func TestSessionRejectedForDisabledAccount(t *testing.T) {
	srv := startTestServer(t)
	me := srv.newUser(t, "me")
	require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "me").Update("active", false).Error)
	status, _ := srv.get(t, "/api/v1/user/profile", me.session)
	assert.NotEqual(t, http.StatusOK, status)
}

func (s *testServer) userID(t *testing.T, username string) string {
	t.Helper()
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("username = ?", username).First(&u).Error)
	return u.Id
}

func (s *testServer) countRows(t *testing.T, model any, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.deps.Database.Db.Model(model).Where(where, args...).Count(&n).Error)
	return n
}

// repoDir is where local storage keeps the repository's git data (its
// storage key, not its path). path is "<namespace>/<slug>".
func (s *testServer) repoDir(t *testing.T, path string) string {
	t.Helper()
	namespace, slug, ok := strings.Cut(path, "/")
	require.True(t, ok, "repository path %q", path)
	var repo domain.Repository
	require.NoError(t, s.deps.Database.Db.Where("namespace = ? AND slug = ?", namespace, slug).First(&repo).Error)
	require.NotEmpty(t, repo.StorageKey)
	return filepath.Join(s.deps.Config.Storage.Local.Path, repo.StorageKey+".git")
}
