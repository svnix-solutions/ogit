//go:build integration

package server

import (
	"net/http"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/sso/ldaptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDirectoryWithOpenLDAP signs in against a real OpenLDAP: the user
// search, the user's bind, groups searched under ou=groups, StartTLS.
func TestDirectoryWithOpenLDAP(t *testing.T) {
	dir := ldaptest.StartOpenLDAP(t,
		ldaptest.Entry{DN: "uid=jdoe,ou=people,dc=example,dc=org", Password: directoryPassword, Attrs: map[string][]string{
			"objectClass": {"inetOrgPerson"}, "uid": {"jdoe"}, "cn": {"Jane Doe"}, "sn": {"Doe"}, "mail": {"jdoe@example.org"}}},
		ldaptest.Entry{DN: "cn=ogit-admins,ou=groups,dc=example,dc=org", Attrs: map[string][]string{
			"objectClass": {"groupOfNames"}, "cn": {"ogit-admins"}, "member": {"uid=jdoe,ou=people,dc=example,dc=org"}}},
	)
	for _, startTLS := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "starttls"}[startTLS], func(t *testing.T) {
			srv := startTestServer(t)
			srv.ssoProviders(t, config.SSOProviderConfig{Slug: "openldap", Name: "OpenLDAP", Kind: "ldap", Issuer: dir.URL,
				ClientId: dir.AdminDN, ClientSecret: dir.AdminPassword, AdminGroup: "ogit-admins",
				LDAP: &domain.LDAPSettings{UserBase: "ou=people,dc=example,dc=org", SubjectAttribute: "entryUUID", GroupBase: "ou=groups,dc=example,dc=org",
					StartTLS: startTLS, InsecureSkipVerify: startTLS}}) // the container's certificate is self-signed
			status, body := srv.signIn(t, "jdoe", "wrong-Password-1")
			assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
			status, body = srv.signIn(t, "jdoe", directoryPassword)
			require.Equal(t, http.StatusOK, status, "%v", body)
			_, me := srv.get(t, "/api/v1/user/profile", body["token"].(string))
			assert.Equal(t, "jdoe", me["username"])
			assert.Equal(t, "jdoe@example.org", me["email"])
			u := srv.userNamed(t, "jdoe")
			assert.Equal(t, domain.RoleAdmin, u.Role, "the group search found ogit-admins")
			var identity domain.UserIdentity
			require.NoError(t, srv.deps.Database.Db.Where("user_id = ?", u.Id).First(&identity).Error)
			assert.Contains(t, identity.Subject, "entryUUID:", "OpenLDAP's entryUUID")
		})
	}
}
