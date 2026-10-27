package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// member returns username's membership in group (nil: none).
func (s *testServer) member(t *testing.T, group, username string) *domain.GroupMember {
	t.Helper()
	g, err := s.deps.GroupApp.GroupPers.GetByName(group)
	require.NoError(t, err)
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("username = ?", username).First(&u).Error)
	m, err := s.deps.GroupApp.GroupPers.GetMember(g.Id, u.Id)
	if err != nil {
		return nil
	}
	return m
}

func (s *testServer) userNamed(t *testing.T, username string) domain.User {
	t.Helper()
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("username = ?", username).First(&u).Error)
	return u
}

func TestSSOGroupSync(t *testing.T) {
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "corp", func(p *config.SSOProviderConfig) {
		p.GroupsClaim, p.AdminGroup = "realm_access.roles", "ogit-admins"
		p.GroupMappings = append(p.GroupMappings,
			struct {
				Group  string `yaml:"group"`
				Target string `yaml:"target"`
				Role   string `yaml:"role"`
			}{"leads", "engineering", "maintainer"},
			struct {
				Group  string `yaml:"group"`
				Target string `yaml:"target"`
				Role   string `yaml:"role"`
			}{"eng", "engineering", "member"})
	}))
	owner, carol := srv.newUser(t, "owner"), srv.newUser(t, "carol")
	status, body := srv.post(t, "/api/v1/group", owner.session, map[string]any{"name": "engineering"})
	require.Equal(t, http.StatusOK, status, "%v", body)

	signIn := func(sub, username string, groups ...string) {
		t.Helper()
		idp.signInAs(idpUser{sub: sub, email: username + "@corp.example", username: username, verified: true, groups: groups})
		loc := newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")
		require.Equal(t, "/", loc.Path, "%v", loc)
	}

	t.Run("memberships follow the provider's groups", func(t *testing.T) {
		signIn("d-1", "dana", "eng", "other")
		m := srv.member(t, "engineering", "dana")
		require.NotNil(t, m, "added")
		assert.Equal(t, domain.GroupRoleMember, m.Role)
		require.NotNil(t, m.SyncedFrom)
		assert.Equal(t, "corp", *m.SyncedFrom)

		signIn("d-1", "dana", "eng", "leads")
		assert.Equal(t, domain.GroupRoleMaintainer, srv.member(t, "engineering", "dana").Role, "the highest mapped role")
		signIn("d-1", "dana")
		assert.Nil(t, srv.member(t, "engineering", "dana"), "removed when they leave")
	})

	t.Run("hand-made memberships are left alone", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/group/engineering/members", owner.session, map[string]any{"username": "carol", "role": "maintainer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		idp.signInAs(idpUser{sub: "c-1", email: "carol@example.com", username: "carol", verified: true, groups: []string{"eng"}})
		b := newBrowser(t, srv)
		token := strings.TrimPrefix(b.sso("/api/v1/auth/sso/corp/start").Fragment, "token=")
		status, body, _ = b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		require.Equal(t, http.StatusOK, status, "%v", body)
		m := srv.member(t, "engineering", "carol")
		assert.Equal(t, domain.GroupRoleMaintainer, m.Role, "not downgraded")
		assert.Nil(t, m.SyncedFrom)
		idp.signInAs(idpUser{sub: "c-1", email: "carol@example.com", username: "carol", verified: true})
		newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")
		assert.NotNil(t, srv.member(t, "engineering", "carol"), "not removed")
		_ = carol
	})

	t.Run("linking by email syncs too", func(t *testing.T) {
		srv.newUser(t, "erin")
		idp.signInAs(idpUser{sub: "e-1", email: "erin@example.com", username: "erin", verified: true, groups: []string{"eng"}})
		b := newBrowser(t, srv)
		token := strings.TrimPrefix(b.sso("/api/v1/auth/sso/corp/start").Fragment, "token=")
		assert.Nil(t, srv.member(t, "engineering", "erin"), "not before the link is confirmed")
		status, body, _ := b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotNil(t, srv.member(t, "engineering", "erin"))
	})

	t.Run("site admin", func(t *testing.T) {
		signIn("a-1", "ada", "ogit-admins")
		ada := srv.userNamed(t, "ada")
		assert.Equal(t, domain.RoleAdmin, ada.Role)
		assert.True(t, ada.RoleSynced)

		// owner signs in through the provider too, then becomes an admin
		// by hand: sync never takes that back.
		idp.signInAs(idpUser{sub: "own-1", email: "owner@example.com", username: "owner", verified: true})
		b := newBrowser(t, srv)
		token := strings.TrimPrefix(b.sso("/api/v1/auth/sso/corp/start").Fragment, "token=")
		status, body, _ := b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.promote(t, "owner")
		signIn("a-1", "ada")
		assert.Equal(t, domain.RoleUser, srv.userNamed(t, "ada").Role, "revoked when they leave the group")
		signIn("a-1", "ada", "ogit-admins") // two admins again
		idp.signInAs(idpUser{sub: "own-1", email: "owner@example.com", username: "owner", verified: true})
		newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")
		assert.Equal(t, domain.RoleAdmin, srv.userNamed(t, "owner").Role, "a hand-made admin stays one")

		// An admin setting ada's role by hand makes it hand-made.
		signIn("b-1", "bea", "ogit-admins")
		status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/users/bea", owner.session, map[string]any{"role": "admin"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.False(t, srv.userNamed(t, "bea").RoleSynced)
		signIn("b-1", "bea")
		assert.Equal(t, domain.RoleAdmin, srv.userNamed(t, "bea").Role, "no longer the provider's to take back")
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "bea").Update("role", domain.RoleUser).Error)

		// Never the last active admin.
		signIn("a-1", "ada", "ogit-admins")
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "owner").Update("role", domain.RoleUser).Error)
		signIn("a-1", "ada")
		assert.Equal(t, domain.RoleAdmin, srv.userNamed(t, "ada").Role, "kept: the last admin")
	})

	t.Run("mappings are checked", func(t *testing.T) {
		srv.promote(t, "owner")
		for _, bad := range [][]map[string]any{
			{{"group": "x", "target": "engineering", "role": "owner"}},
			{{"group": "", "target": "engineering", "role": "member"}},
			{{"group": "x", "target": "", "role": "member"}},
		} {
			status, body := srv.post(t, "/api/v1/admin/sso/providers", owner.session, map[string]any{"slug": "bad", "name": "Bad", "kind": "oidc",
				"issuer": idp.srv.URL, "client_id": "x", "client_secret": "x", "group_mappings": bad})
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", bad, body)
		}
		status, body := srv.post(t, "/api/v1/admin/sso/providers", owner.session, map[string]any{"slug": "good", "name": "Good", "kind": "oidc",
			"issuer": idp.srv.URL, "client_id": "x", "client_secret": "x", "admin_group": "admins",
			"group_mappings": []map[string]any{{"group": "x", "target": "engineering", "role": "maintainer"}}})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, []any{map[string]any{"group": "x", "target": "engineering", "role": "maintainer"}}, body["group_mappings"])
	})
}

func TestSSOOnly(t *testing.T) {
	// No withPublicURL: an https public URL makes the sign-in cookie
	// Secure, which Go before 1.26 doesn't send to http://127.0.0.1
	// (TestSSOOnlyPasskeys needs it).
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "corp"))
	srv.deps.SSOApp.Config.Security.SSOOnlyExempt = []string{"root"}
	root, boss := srv.newUser(t, "root"), srv.newUser(t, "boss")
	srv.newUser(t, "alice")
	srv.promote(t, "root")
	srv.promote(t, "boss")
	// alice also signs in through the provider.
	idp.signInAs(idpUser{sub: "al-1", email: "alice@example.com", username: "alice", verified: true})
	b := newBrowser(t, srv)
	token := strings.TrimPrefix(b.sso("/api/v1/auth/sso/corp/start").Fragment, "token=")
	status, body, _ := b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
	require.Equal(t, http.StatusOK, status, "%v", body)
	// carl, without 2FA, confirms sudo with his password today, and signs
	// in through the provider too.
	carl := srv.newUser(t, "carl")
	idp.signInAs(idpUser{sub: "ca-1", email: "carl@example.com", username: "carl", verified: true})
	cb := newBrowser(t, srv)
	token = strings.TrimPrefix(cb.sso("/api/v1/auth/sso/corp/start").Fragment, "token=")
	status, body, _ = cb.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, profile := srv.get(t, "/api/v1/user/profile", carl.session)
	assert.Equal(t, false, profile["two_factor_satisfied"], "his password signs in: not yet")

	status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", boss.session, map[string]any{"sso_only": true})
	assert.Equal(t, http.StatusConflict, status, "%v", body)
	assert.Equal(t, "WOULD_LOCK_OUT", body["type"], "boss has no way in then")
	status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", root.session, map[string]any{"sso_only": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, true, body["sso_only"])
	assert.Equal(t, []any{"root"}, body["sso_only_exempt"])

	login := func(email string) (int, map[string]any) {
		return srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": email, "password": testPassword})
	}
	status, body = login("alice@example.com")
	assert.Equal(t, http.StatusForbidden, status, "%v", body)
	assert.Equal(t, "PASSWORD_LOGIN_DISABLED", body["type"])
	status, _ = login("root@example.com")
	assert.Equal(t, http.StatusOK, status, "the break-glass account")
	status, _ = srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "alice@example.com", "password": "wrong-Password-1"})
	assert.Equal(t, http.StatusUnauthorized, status, "a wrong password still says nothing about the account")

	status, _ = srv.post(t, "/api/v1/auth/register", "", map[string]any{"username": "newbie", "email": "newbie@example.com", "password": testPassword})
	assert.NotEqual(t, http.StatusOK, status, "no registration")
	_, instance := srv.get(t, "/api/v1/instance", "")
	assert.Equal(t, true, instance["sso_only"])
	assert.Equal(t, false, instance["registration_enabled"])

	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
	status, body = srv.post(t, "/api/v1/auth/sudo", carl.session, map[string]any{"password": testPassword})
	assert.Equal(t, http.StatusForbidden, status, "no password sudo: %v", body)
	assert.Equal(t, "PASSWORD_LOGIN_DISABLED", body["type"])
	_, profile = srv.get(t, "/api/v1/user/profile", carl.session)
	assert.Equal(t, false, profile["password_login"])
	assert.Equal(t, true, profile["two_factor_satisfied"], "his password can't be used: the provider's MFA counts")

	idp.signInAs(idpUser{sub: "al-1", email: "alice@example.com", username: "alice", verified: true})
	assert.Equal(t, "/", newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start").Path, "providers still sign in")

	status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", root.session, map[string]any{"sso_only": false})
	require.Equal(t, http.StatusForbidden, status, "sudo (expired above)")
	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(time.Minute)).Error)
	status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/settings", root.session, map[string]any{"sso_only": false})
	require.Equal(t, http.StatusOK, status)
	status, _ = login("alice@example.com")
	assert.Equal(t, http.StatusOK, status, "passwords are back")
}

// TestSSOOnlyPasskeys: passkeys don't sign in either (but the exempt's
// do).
func TestSSOOnlyPasskeys(t *testing.T) {
	srv := startTestServer(t, withPublicURL)
	alice, root := srv.newUser(t, "alice"), srv.newUser(t, "root")
	alicesKey, rootsKey := newSoftKey(t), newSoftKey(t)
	srv.addKey(t, alice, alicesKey, "Phone", true)
	srv.addKey(t, root, rootsKey, "Phone", true)
	srv.deps.SSOApp.Config.Security.SSOOnly = true
	srv.deps.SSOApp.Config.Security.SSOOnlyExempt = []string{"root"}
	signIn := func(k *softKey) (int, map[string]any) {
		t.Helper()
		status, begin := srv.post(t, "/api/v1/auth/passkey", "", nil)
		require.Equal(t, http.StatusOK, status, "%v", begin)
		return srv.post(t, "/api/v1/auth/passkey/finish", "", map[string]any{"ceremony": begin["ceremony"], "credential": k.assert(t, begin["options"])})
	}
	status, body := signIn(alicesKey)
	assert.Equal(t, http.StatusForbidden, status, "%v", body)
	assert.Equal(t, "PASSWORD_LOGIN_DISABLED", body["type"])
	status, body = signIn(rootsKey)
	assert.Equal(t, http.StatusOK, status, "the break-glass account: %v", body)
}

func TestSSOOnlyNeedsAProvider(t *testing.T) {
	srv := startTestServer(t)
	srv.ssoProviders(t)
	root := srv.newUser(t, "root")
	srv.promote(t, "root")
	srv.deps.SSOApp.Config.Security.SSOOnlyExempt = []string{"root"}
	status, body := srv.request(t, http.MethodPatch, "/api/v1/admin/settings", root.session, map[string]any{"sso_only": true})
	assert.Equal(t, http.StatusConflict, status, "%v", body)
	assert.Equal(t, "NO_SIGN_IN_PROVIDER", body["type"])
}
