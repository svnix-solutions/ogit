//go:build integration

package server

import (
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/labbs/ogit/infrastructure/sso/samltest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSAMLWithKeycloak signs in through a real Keycloak: our signed
// AuthnRequest (redirect binding), Keycloak's login form, its signed
// response posted back, the browser's finish step.
func TestSAMLWithKeycloak(t *testing.T) {
	kc := samltest.StartKeycloak(t)
	srv := startTestServer(t)
	srv.ssoProviders(t)
	root := srv.newUser(t, "root")
	srv.promote(t, "root")
	srv.samlProvider(t, root, "keycloak", nil, map[string]any{"issuer": kc.MetadataURL(), "groups_claim": "", "admin_group": ""})
	kc.AddSAMLClient(srv.url+"/api/v1/auth/sso/keycloak/metadata", srv.url+"/api/v1/auth/sso/keycloak/acs")
	kc.AddUser("kim", "kim@corp.example", "Keycloak-Pass-1")

	b := newBrowser(t, srv)
	client := b.noRedirects()
	resp, err := client.Get(srv.url + "/api/v1/auth/sso/keycloak/start?redirect=/explore")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	// Keycloak's login page (its own redirects followed).
	follow := &http.Client{Jar: b.http.Jar}
	resp, err = follow.Get(resp.Header.Get("Location"))
	require.NoError(t, err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	action, ok := samltest.LoginForm(page)
	require.True(t, ok, "no login form: %s", page)
	resp, err = follow.PostForm(action, url.Values{"username": {"kim"}, "password": {"Keycloak-Pass-1"}, "credentialId": {""}})
	require.NoError(t, err)
	page, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	acs, fields := samlForm(t, page)
	assert.Equal(t, srv.url+"/api/v1/auth/sso/keycloak/acs", acs)
	loc := b.postForm(acs, fields)
	assert.Equal(t, "/explore", loc.RequestURI(), "%v", loc)
	_, me := b.profile()
	assert.Equal(t, "kim", me["username"])
	assert.Equal(t, "kim@corp.example", me["email"])
}
