package sso

import (
	"context"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/sso/ldaptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func directory(t *testing.T) *ldaptest.Server {
	srv := ldaptest.Start(t,
		ldaptest.Entry{DN: "uid=jdoe,ou=people,dc=example,dc=org", Password: "s3cret", Attrs: map[string][]string{
			"objectClass": {"person", "inetOrgPerson"}, "uid": {"jdoe"}, "mail": {"jdoe@example.org"}, "cn": {"Jane Doe"},
			"entryUUID": {"5f8b0a4e-1111-2222-3333-444455556666"}, "memberOf": {"cn=eng,ou=groups,dc=example,dc=org", "cn=ogit-admins,ou=groups,dc=example,dc=org"}}},
		ldaptest.Entry{DN: "uid=twin,ou=people,dc=example,dc=org", Password: "x", Attrs: map[string][]string{
			"objectClass": {"person"}, "uid": {"twin"}, "mail": {"same@example.org"}}},
		ldaptest.Entry{DN: "uid=twin2,ou=people,dc=example,dc=org", Password: "x", Attrs: map[string][]string{
			"objectClass": {"person"}, "uid": {"twin2"}, "mail": {"same@example.org"}}},
		ldaptest.Entry{DN: "cn=eng,ou=groups,dc=example,dc=org", Attrs: map[string][]string{
			"objectClass": {"groupOfNames"}, "cn": {"eng"}, "member": {"uid=jdoe,ou=people,dc=example,dc=org"}}},
		ldaptest.Entry{DN: "cn=ops,ou=groups,dc=example,dc=org", Attrs: map[string][]string{
			"objectClass": {"groupOfNames"}, "cn": {"ops"}, "member": {"uid=someone,ou=people,dc=example,dc=org"}}},
	)
	srv.ServiceDN, srv.ServicePassword = "cn=ogit,dc=example,dc=org", "service"
	return srv
}

func TestLDAPAuthenticate(t *testing.T) {
	srv := directory(t)
	d := LDAP{URL: srv.URL, BindDN: srv.ServiceDN, BindPassword: srv.ServicePassword,
		Settings: domain.LDAPSettings{UserBase: "ou=people,dc=example,dc=org", SubjectAttribute: "entryUUID", GroupBase: "ou=groups,dc=example,dc=org"}}
	ctx := context.Background()

	for _, input := range []string{"jdoe", "JDOE", "jdoe@example.org"} {
		c, err := d.Authenticate(ctx, input, "s3cret")
		require.NoError(t, err, input)
		assert.Equal(t, "entryUUID:5f8b0a4e-1111-2222-3333-444455556666", c.Subject)
		assert.Equal(t, "jdoe", c.Username)
		assert.Equal(t, "jdoe@example.org", c.Email)
		assert.True(t, c.EmailVerified)
		assert.Equal(t, "Jane Doe", c.Name)
		assert.Equal(t, []string{"eng"}, c.Groups, "searched under group_base")
	}

	_, err := d.Authenticate(ctx, "jdoe", "wrong")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials)
	_, err = d.Authenticate(ctx, "nobody", "s3cret")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials, "the same answer for an unknown user")
	_, err = d.Authenticate(ctx, "same@example.org", "x")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials, "two matches: nobody")
	_, err = d.Authenticate(ctx, "*", "s3cret")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials, "the input is escaped, not a wildcard")
	_, err = d.Authenticate(ctx, "jdoe)(uid=*", "s3cret")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials, "no filter injection")

	before := len(srv.Binds())
	_, err = d.Authenticate(ctx, "jdoe", "")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials)
	assert.Len(t, srv.Binds(), before, "an empty password never reaches the directory")
	for _, b := range srv.Binds() {
		assert.NotContains(t, b, "empty password")
	}

	d.Settings.GroupBase, d.Settings.MemberOfAttribute = "", "memberOf"
	c, err := d.Authenticate(ctx, "jdoe", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, []string{"eng", "ogit-admins"}, c.Groups, "from memberOf")

	d.BindPassword = "wrong"
	_, err = d.Authenticate(ctx, "jdoe", "s3cret")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrLDAPInvalidCredentials, "a broken service account is the server's problem")
}

func TestLDAPSubjectDefaultsToTheDN(t *testing.T) {
	srv := directory(t)
	d := LDAP{URL: srv.URL, BindDN: srv.ServiceDN, BindPassword: srv.ServicePassword, Settings: domain.LDAPSettings{UserBase: "dc=example,dc=org"}}
	c, err := d.Authenticate(context.Background(), "jdoe", "s3cret")
	require.NoError(t, err)
	assert.Equal(t, "uid=jdoe,ou=people,dc=example,dc=org", c.Subject)
}

func TestCheckLDAP(t *testing.T) {
	ok := domain.LDAPSettings{UserBase: "dc=x"}
	assert.NoError(t, CheckLDAP("ldaps://ad.example.org", ok))
	assert.Error(t, CheckLDAP("https://ad.example.org", ok))
	assert.Error(t, CheckLDAP("ldap://ad.example.org", domain.LDAPSettings{}), "user_base")
	assert.Error(t, CheckLDAP("ldap://ad.example.org", domain.LDAPSettings{UserBase: "dc=x", UserFilter: "(uid=fixed)"}), "{input}")
	assert.Error(t, CheckLDAP("ldap://ad.example.org", domain.LDAPSettings{UserBase: "dc=x", UserFilter: "(uid={input}"}), "a broken filter")
	assert.Error(t, CheckLDAP("ldap://ad.example.org", domain.LDAPSettings{UserBase: "dc=x", RootCA: "not pem"}))
}

// With one person in the directory, an unescaped "*" would be a presence
// filter that finds them.
func TestLDAPInputIsEscaped(t *testing.T) {
	srv := ldaptest.Start(t, ldaptest.Entry{DN: "uid=solo,dc=example,dc=org", Password: "pw", Attrs: map[string][]string{
		"objectClass": {"person"}, "uid": {"solo"}, "mail": {"solo@example.org"}}})
	d := LDAP{URL: srv.URL, Settings: domain.LDAPSettings{UserBase: "dc=example,dc=org"}}
	_, err := d.Authenticate(context.Background(), "solo", "pw")
	require.NoError(t, err)
	_, err = d.Authenticate(context.Background(), "*", "pw")
	assert.ErrorIs(t, err, ErrLDAPInvalidCredentials)
}
