package server

import (
	"net/http"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionUser registers and signs in, without creating an access token
// (refused while 2FA is required and missing).
func (s *testServer) sessionUser(t *testing.T, name string) user {
	t.Helper()
	return user{name: name, session: s.registerAndLogin(t, name+"@example.com", name)}
}

// myRepos lists the caller's repositories (the dashboard).
func (s *testServer) myRepos(t *testing.T, as user) []string {
	t.Helper()
	status, body := s.get(t, "/api/v1/repository", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	return repoPaths(body)
}

func TestRequireTwoFactorSiteWide(t *testing.T) {
	srv := startTestServer(t)
	admin := srv.newUser(t, "root")
	srv.enableTOTP(t, admin)
	srv.promote(t, "root")
	srv.createRepo(t, admin.session, "Site", true)
	dave := srv.newUser(t, "dave")
	status, body := srv.post(t, repoAPI("root/site")+"/members", admin.session, map[string]any{"username": "dave", "role": "developer"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	ci := srv.repoToken(t, admin, "root/site", "ci", "read:repository")
	port := srv.startSSH(t)
	key := newSSHKey(t, "dave@laptop")
	status, body = srv.post(t, "/api/v1/user/ssh-keys", dave.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)

	status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", dave.session, map[string]any{"require_two_factor": true})
	assert.Equal(t, http.StatusForbidden, status, "admins only: %v", body)
	status, _ = srv.get(t, "/api/v1/repository", dave.session)
	assert.Equal(t, http.StatusOK, status, "and nothing changed")
	status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", admin.session, map[string]any{"require_two_factor": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["require_two_factor"])
	assert.Equal(t, false, body["require_two_factor_forced"])

	t.Run("only_the_setup", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository", dave.session)
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "TWO_FACTOR_REQUIRED", body["type"])
		status, body = srv.get(t, repoAPI("root/site"), dave.session)
		assert.Equal(t, "TWO_FACTOR_REQUIRED", body["type"], "%d", status)
		status, profile := srv.get(t, "/api/v1/user/profile", dave.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, profile["two_factor_required"])
		status, _ = srv.get(t, "/api/v1/user/2fa", dave.session)
		assert.Equal(t, http.StatusOK, status, "the setup stays reachable")

		_, err := clone(t, srv.url+"/root/site.git", dave.git)
		require.Error(t, err, "git with an access token")
		assert.Contains(t, err.Error(), "two-factor authentication is required")
		status, body, _ = srv.do(t, http.MethodGet, releasesAPI("root/site"), tokenHeader(dave.git.Password), nil)
		assert.Equal(t, http.StatusForbidden, status, "API with an access token: %v", body)
		assert.Equal(t, "TWO_FACTOR_REQUIRED", body["type"])
		out, err := g.run(t.TempDir(), "clone", g.url("root/site"), ".")
		require.Error(t, err, "git over SSH")
		assert.Contains(t, out, "two-factor authentication is required")

		_, err = clone(t, srv.url+"/root/site.git", &githttp.BasicAuth{Username: "ci", Password: ci["token"].(string)})
		assert.NoError(t, err, "repository tokens are not people")
		status, _ = srv.get(t, "/api/v1/repository", admin.session)
		assert.Equal(t, http.StatusOK, status, "users with 2FA are not concerned")
	})

	t.Run("after_enrolling", func(t *testing.T) {
		srv.enableTOTP(t, dave)
		status, _ := srv.get(t, repoAPI("root/site"), dave.session)
		assert.Equal(t, http.StatusOK, status)
		_, profile := srv.get(t, "/api/v1/user/profile", dave.session)
		assert.Equal(t, false, profile["two_factor_required"])
		_, err := clone(t, srv.url+"/root/site.git", dave.git)
		assert.NoError(t, err)
	})

	t.Run("off_again", func(t *testing.T) {
		erin := srv.sessionUser(t, "erin")
		status, body := srv.post(t, "/api/v1/user/token", erin.session, map[string]any{"name": "x", "scopes": []string{"read:repository"}})
		assert.Equal(t, "TWO_FACTOR_REQUIRED", body["type"], "no new access token before enrolling: %d", status)
		status, _ = srv.get(t, "/api/v1/repository", erin.session)
		require.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", admin.session, map[string]any{"require_two_factor": false})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, "/api/v1/repository", erin.session)
		assert.Equal(t, http.StatusOK, status)
	})
}

func TestRequireTwoFactorForcedByConfig(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) { c.Security.RequireTwoFactor = true })
	// Users can still register and sign in to set it up.
	admin := srv.sessionUser(t, "root")
	srv.enableTOTP(t, admin)
	srv.promote(t, "root")
	status, body := srv.request(t, http.MethodPatch, "/api/v1/admin/settings", admin.session, map[string]any{"require_two_factor": false})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["require_two_factor"], "the configuration wins")
	assert.Equal(t, true, body["require_two_factor_forced"])
	frank := srv.sessionUser(t, "frank")
	status, body = srv.get(t, "/api/v1/repository", frank.session)
	assert.Equal(t, "TWO_FACTOR_REQUIRED", body["type"], "%d", status)
}

func TestAdminsNeedTwoFactor(t *testing.T) {
	srv := startTestServer(t)
	gina := srv.newUser(t, "gina")
	require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "gina").Update("role", domain.RoleAdmin).Error)
	status, body := srv.get(t, "/api/v1/admin/users", gina.session)
	assert.Equal(t, http.StatusForbidden, status)
	assert.Equal(t, "ADMIN_TWO_FACTOR_REQUIRED", body["type"])
	srv.enableTOTP(t, gina)
	status, body = srv.get(t, "/api/v1/admin/users", gina.session)
	assert.Equal(t, http.StatusOK, status, "%v", body)
}

func TestGroupRequiresTwoFactor(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	srv.enableTOTP(t, alice)
	erin := srv.newUser(t, "erin")   // no 2FA
	frank := srv.newUser(t, "frank") // with 2FA
	srv.enableTOTP(t, frank)
	hank := srv.newUser(t, "hank") // a plain member

	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "secure"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	for name, role := range map[string]string{"erin": "maintainer", "frank": "member", "hank": "member"} {
		status, body = srv.post(t, "/api/v1/group/secure/members", alice.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	status, _ = srv.createIn(t, alice, "secure", "Vault", true)
	require.Equal(t, http.StatusOK, status)
	srv.createRepo(t, alice.session, "Notes", true)
	status, body = srv.post(t, repoAPI("alice/notes")+"/members", alice.session, map[string]any{"group": "secure", "role": "developer"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	pushFiles(t, srv.url+"/secure/vault.git", alice.git, "main", "init", map[string]string{"a.txt": "a\n"})

	assert.ElementsMatch(t, []string{"secure/vault", "alice/notes"}, srv.myRepos(t, erin), "before")

	status, body = srv.request(t, http.MethodPatch, "/api/v1/group/secure", erin.session, map[string]any{"require_two_factor": true})
	assert.Equal(t, http.StatusForbidden, status, "owner only: %v", body)
	status, body = srv.request(t, http.MethodPatch, "/api/v1/group/secure", alice.session, map[string]any{"require_two_factor": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["require_two_factor"])

	t.Run("without_2fa", func(t *testing.T) {
		assert.Empty(t, srv.myRepos(t, erin))
		for _, repo := range []string{"secure/vault", "alice/notes"} {
			status, _ := srv.get(t, repoAPI(repo), erin.session)
			assert.Equal(t, http.StatusNotFound, status, "%s: the group's access counts as none", repo)
		}
		_, err := clone(t, srv.url+"/secure/vault.git", erin.git)
		assert.Error(t, err, "git too")
		status, body := srv.createIn(t, erin, "secure", "More", true)
		assert.Equal(t, "GROUP_REQUIRES_TWO_FACTOR", body["type"], "%d", status)
		status, _ = srv.get(t, "/api/v1/group/secure", erin.session)
		assert.Equal(t, http.StatusOK, status, "still a member")
	})

	t.Run("with_2fa", func(t *testing.T) {
		assert.ElementsMatch(t, []string{"secure/vault", "alice/notes"}, srv.myRepos(t, frank))
		_, err := clone(t, srv.url+"/secure/vault.git", frank.git)
		assert.NoError(t, err)
	})

	t.Run("who_lacks_2fa", func(t *testing.T) {
		_, body := srv.get(t, "/api/v1/group/secure/members", alice.session)
		flags := map[string]any{}
		for _, m := range body["members"].([]any) {
			flags[m.(map[string]any)["username"].(string)] = m.(map[string]any)["two_factor_enabled"]
		}
		assert.Equal(t, map[string]any{"alice": true, "erin": false, "frank": true, "hank": false}, flags)
		_, body = srv.get(t, "/api/v1/group/secure/members", hank.session)
		assert.NotContains(t, body["members"].([]any)[0], "two_factor_enabled", "members don't see it")
	})

	t.Run("enrolling_restores_access", func(t *testing.T) {
		srv.enableTOTP(t, erin)
		assert.ElementsMatch(t, []string{"secure/vault", "alice/notes"}, srv.myRepos(t, erin))
		status, _ := srv.createIn(t, erin, "secure", "More", true)
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("turned_off", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, "/api/v1/group/secure", alice.session, map[string]any{"require_two_factor": false})
		require.Equal(t, http.StatusOK, status)
		assert.ElementsMatch(t, []string{"secure/vault", "secure/more", "alice/notes"}, srv.myRepos(t, hank))
	})
}
