//go:build integration

package ldaptest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-ldap/ldap/v3"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// openLDAPImage is pinned by digest (amd64 and arm64).
const openLDAPImage = "osixia/openldap:1.5.0@sha256:18742e9c449c9c1afe129d3f2f3ee15fb34cc43e5f940a20f3399728f41d7c28"

// OpenLDAP is a running OpenLDAP container for dc=example,dc=org (StartTLS
// with a self-signed certificate).
type OpenLDAP struct {
	URL                    string
	AdminDN, AdminPassword string
}

// StartOpenLDAP starts OpenLDAP (requires Docker) with entries added; it
// is removed when the test ends.
func StartOpenLDAP(t testing.TB, entries ...Entry) *OpenLDAP {
	t.Helper()
	ctx := context.Background()
	o := &OpenLDAP{AdminDN: "cn=admin,dc=example,dc=org", AdminPassword: "admin-secret"}
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        openLDAPImage,
			ExposedPorts: []string{"389/tcp"},
			// The image demands client certificates for TLS by default.
			Env: map[string]string{"LDAP_ORGANISATION": "Example", "LDAP_DOMAIN": "example.org", "LDAP_ADMIN_PASSWORD": o.AdminPassword,
				"LDAP_TLS_VERIFY_CLIENT": "never"},
			// The image configures itself with a first slapd, then starts
			// the real one, which logs "slapd starting".
			WaitingFor: wait.ForAll(wait.ForListeningPort("389/tcp"), wait.ForLog("slapd starting")).WithDeadline(2 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "389/tcp")
	require.NoError(t, err)
	o.URL = fmt.Sprintf("ldap://%s:%s", host, port.Port())

	// slapd listens before it takes binds: retry for a while.
	var conn *ldap.Conn
	require.Eventually(t, func() bool {
		if conn, err = ldap.DialURL(o.URL); err != nil {
			return false
		}
		if err = conn.Bind(o.AdminDN, o.AdminPassword); err != nil {
			conn.Close()
			return false
		}
		return true
	}, time.Minute, time.Second, "OpenLDAP never took the admin's bind: %v", err)
	defer conn.Close()
	for _, ou := range []string{"people", "groups"} {
		add := ldap.NewAddRequest("ou="+ou+",dc=example,dc=org", nil)
		add.Attribute("objectClass", []string{"organizationalUnit"})
		add.Attribute("ou", []string{ou})
		require.NoError(t, conn.Add(add))
	}
	for _, e := range entries {
		add := ldap.NewAddRequest(e.DN, nil)
		for k, v := range e.Attrs {
			add.Attribute(k, v)
		}
		if e.Password != "" {
			add.Attribute("userPassword", []string{e.Password})
		}
		require.NoError(t, conn.Add(add), e.DN)
	}
	return o
}
