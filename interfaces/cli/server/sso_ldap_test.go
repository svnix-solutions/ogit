package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/sso/ldaptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const directoryPassword = "Directory-Pass-1"

func person(uid, mail, password string, groups ...string) ldaptest.Entry {
	var memberOf []string
	for _, g := range groups {
		memberOf = append(memberOf, "cn="+g+",ou=groups,dc=example,dc=org")
	}
	return ldaptest.Entry{DN: "uid=" + uid + ",ou=people,dc=example,dc=org", Password: password, Attrs: map[string][]string{
		"objectClass": {"person", "inetOrgPerson"}, "uid": {uid}, "mail": {mail}, "cn": {uid}, "entryUUID": {"uuid-" + uid}, "memberOf": memberOf}}
}

func startDirectory(t *testing.T, srv *testServer, entries ...ldaptest.Entry) *ldaptest.Server {
	dir := ldaptest.Start(t, entries...)
	dir.ServiceDN, dir.ServicePassword = "cn=ogit,dc=example,dc=org", "service-pw"
	srv.ssoProviders(t, config.SSOProviderConfig{Slug: "corp-ad", Name: "Corp directory", Kind: "ldap", Issuer: dir.URL,
		ClientId: dir.ServiceDN, ClientSecret: dir.ServicePassword, AdminGroup: "ogit-admins",
		LDAP: &domain.LDAPSettings{UserBase: "ou=people,dc=example,dc=org", SubjectAttribute: "entryUUID", MemberOfAttribute: "memberOf"}})
	return dir
}

func (s *testServer) signIn(t *testing.T, name, password string) (int, map[string]any) {
	t.Helper()
	return s.post(t, "/api/v1/auth/login", "", map[string]any{"email": name, "password": password})
}

func TestDirectorySignIn(t *testing.T) {
	srv := startTestServer(t)
	srv.newUser(t, "alice")
	root := srv.newUser(t, "root")
	dir := startDirectory(t, srv,
		person("jdoe", "jdoe@corp.example", directoryPassword, "ogit-admins"),
		person("ally", "alice@example.com", directoryPassword))

	_, instance := srv.get(t, "/api/v1/instance", "")
	assert.Equal(t, true, instance["directory_sign_in"])
	_, providers := srv.get(t, "/api/v1/auth/providers", "")
	assert.Empty(t, providers["providers"], "directories have no button")

	t.Run("a new account from the directory", func(t *testing.T) {
		status, body := srv.signIn(t, "jdoe", directoryPassword)
		require.Equal(t, http.StatusOK, status, "%v", body)
		token := body["token"].(string)
		_, me := srv.get(t, "/api/v1/user/profile", token)
		assert.Equal(t, "jdoe", me["username"])
		assert.Equal(t, "jdoe@corp.example", me["email"])
		assert.Equal(t, false, me["has_password"])
		assert.Equal(t, true, me["password_login"], "the directory's password")
		assert.Equal(t, false, me["two_factor_satisfied"], "a directory password is one factor")
		assert.Equal(t, domain.RoleAdmin, srv.userNamed(t, "jdoe").Role, "groups sync from memberOf")

		status, body = srv.signIn(t, "jdoe@corp.example", directoryPassword)
		require.Equal(t, http.StatusOK, status, "the email works too: %v", body)
		_, again := srv.get(t, "/api/v1/user/profile", body["token"].(string))
		assert.Equal(t, me["id"], again["id"])
	})

	t.Run("wrong and unknown are the same", func(t *testing.T) {
		status, body := srv.signIn(t, "jdoe", "wrong-Password-1")
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
		assert.Equal(t, "INVALID_CREDENTIALS", body["type"])
		status, body = srv.signIn(t, "nobody", directoryPassword)
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
		assert.Equal(t, "INVALID_CREDENTIALS", body["type"])
	})

	t.Run("local accounts after the directory", func(t *testing.T) {
		status, body := srv.signIn(t, "root@example.com", testPassword)
		assert.Equal(t, http.StatusOK, status, "%v", body)
		_ = root
	})

	t.Run("an existing account links with its password", func(t *testing.T) {
		status, body := srv.signIn(t, "ally", directoryPassword)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Empty(t, body["token"])
		link := body["link_token"].(string)
		require.NotEmpty(t, link)
		status, body = srv.post(t, "/api/v1/auth/sso/link", "", map[string]any{"token": link, "password": testPassword})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.signIn(t, "ally", directoryPassword)
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, me := srv.get(t, "/api/v1/user/profile", body["token"].(string))
		assert.Equal(t, "alice", me["username"])
	})

	t.Run("oGit 2FA is asked", func(t *testing.T) {
		status, body := srv.signIn(t, "ally", directoryPassword)
		require.Equal(t, http.StatusOK, status)
		alice := user{name: "alice", session: body["token"].(string)}
		srv.enableTOTP(t, alice)
		status, body = srv.signIn(t, "ally", directoryPassword)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["two_factor_required"], "%v", body)
		assert.Empty(t, body["token"])
	})

	t.Run("the directory password confirms sudo", func(t *testing.T) {
		_, body := srv.signIn(t, "jdoe", directoryPassword)
		token := body["token"].(string)
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
		status, body := srv.post(t, "/api/v1/auth/sudo", token, map[string]any{"password": "wrong-Password-1"})
		assert.NotEqual(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, "/api/v1/auth/sudo", token, map[string]any{"password": directoryPassword})
		assert.Equal(t, http.StatusOK, status, "%v", body)

		// Someone else now has the name jdoe in the directory: their
		// password confirms nothing for our jdoe.
		impostor := person("jdoe", "jdoe@corp.example", "Impostor-Pass-1")
		impostor.Attrs["entryUUID"] = []string{"uuid-someone-else"}
		dir.SetEntries(impostor, person("ally", "alice@example.com", directoryPassword))
		defer dir.SetEntries(person("jdoe", "jdoe@corp.example", directoryPassword, "ogit-admins"), person("ally", "alice@example.com", directoryPassword))
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
		status, body = srv.post(t, "/api/v1/auth/sudo", token, map[string]any{"password": "Impostor-Pass-1"})
		assert.NotEqual(t, http.StatusOK, status, "%v", body)
	})

	t.Run("only providers sign in: directories still do", func(t *testing.T) {
		srv.deps.SSOApp.Config.Security.SSOOnly = true
		defer func() { srv.deps.SSOApp.Config.Security.SSOOnly = false }()
		status, body := srv.signIn(t, "jdoe", directoryPassword)
		assert.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.signIn(t, "root@example.com", testPassword)
		assert.Equal(t, http.StatusForbidden, status, "local passwords don't: %v", body)
	})

	t.Run("a directory that's down", func(t *testing.T) {
		dir.SetEntries()
		status, _ := srv.signIn(t, "jdoe", directoryPassword)
		assert.Equal(t, http.StatusUnauthorized, status, "gone from the directory")
		srv.deps.SSOApp.FromConfig[0].Issuer = "ldap://127.0.0.1:1" // nothing listens
		status, body := srv.signIn(t, "root@example.com", testPassword)
		assert.Equal(t, http.StatusOK, status, "local accounts still sign in: %v", body)
	})
}

func TestDirectoryRefusals(t *testing.T) {
	srv := startTestServer(t)
	startDirectory(t, srv, person("nomail", "", directoryPassword))
	status, body := srv.signIn(t, "nomail", directoryPassword)
	assert.Equal(t, http.StatusForbidden, status, "%v", body)
	assert.Equal(t, "EMAIL_REQUIRED", body["type"], "no email: no account")
}

func TestDirectoryAdmin(t *testing.T) {
	srv := startTestServer(t)
	srv.ssoProviders(t)
	admin := srv.newUser(t, "root")
	srv.promote(t, "root")
	dir := ldaptest.Start(t, person("kim", "kim@corp.example", directoryPassword))
	for _, bad := range []map[string]any{
		{"slug": "ad", "name": "AD", "kind": "ldap", "issuer": "https://ad.example.org", "ldap": map[string]any{"user_base": "dc=x"}},
		{"slug": "ad", "name": "AD", "kind": "ldap", "issuer": dir.URL, "ldap": map[string]any{}},
		{"slug": "ad", "name": "AD", "kind": "ldap", "issuer": dir.URL, "ldap": map[string]any{"user_base": "dc=x", "user_filter": "(uid=fixed)"}},
	} {
		status, body := srv.post(t, "/api/v1/admin/sso/providers", admin.session, bad)
		assert.Equal(t, http.StatusBadRequest, status, "%v: %v", bad, body)
	}
	status, body := srv.post(t, "/api/v1/admin/sso/providers", admin.session, map[string]any{"slug": "ad", "name": "AD", "kind": "ldap",
		"issuer": dir.URL, "ldap": map[string]any{"user_base": "ou=people,dc=example,dc=org"}})
	require.Equal(t, http.StatusOK, status, "anonymous search, no secret: %v", body)
	assert.Equal(t, "ou=people,dc=example,dc=org", body["ldap"].(map[string]any)["user_base"])
	status, body = srv.signIn(t, "kim", directoryPassword)
	require.Equal(t, http.StatusOK, status, "%v", body)
}
