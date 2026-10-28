//go:build integration

package samltest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// keycloakImage is pinned by digest (amd64 and arm64).
const keycloakImage = "quay.io/keycloak/keycloak:26.4.2@sha256:3617b09bb4b7510a8d8d9b9fc5707399e2d70688dbcc2f8fb013a144829be1b9"

// Keycloak is a running Keycloak (dev mode) with a realm "test".
type Keycloak struct {
	URL   string
	t     testing.TB
	token string
}

// StartKeycloak starts Keycloak (requires Docker) and makes the realm
// "test"; it is removed when the test ends.
func StartKeycloak(t testing.TB) *Keycloak {
	t.Helper()
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        keycloakImage,
			Cmd:          []string{"start-dev"},
			ExposedPorts: []string{"8080/tcp"},
			Env:          map[string]string{"KC_BOOTSTRAP_ADMIN_USERNAME": "admin", "KC_BOOTSTRAP_ADMIN_PASSWORD": "admin-secret"},
			WaitingFor:   wait.ForHTTP("/realms/master").WithPort("8080/tcp").WithStartupTimeout(3 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	port, err := ctr.MappedPort(ctx, "8080/tcp")
	require.NoError(t, err)
	k := &Keycloak{URL: fmt.Sprintf("http://%s:%s", host, port.Port()), t: t}

	resp, err := http.PostForm(k.URL+"/realms/master/protocol/openid-connect/token",
		url.Values{"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"admin"}, "password": {"admin-secret"}})
	require.NoError(t, err)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tok))
	resp.Body.Close()
	require.NotEmpty(t, tok.AccessToken)
	k.token = tok.AccessToken
	k.admin(http.MethodPost, "/admin/realms", map[string]any{"realm": "test", "enabled": true})
	return k
}

func (k *Keycloak) admin(method, path string, body any) {
	k.t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, k.URL+path, bytes.NewReader(b))
	require.NoError(k.t, err)
	req.Header.Set("Authorization", "Bearer "+k.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(k.t, err)
	resp.Body.Close()
	require.Less(k.t, resp.StatusCode, 300, "keycloak %s %s: %s", method, path, resp.Status)
}

// MetadataURL is the realm's SAML IdP metadata.
func (k *Keycloak) MetadataURL() string { return k.URL + "/realms/test/protocol/saml/descriptor" }

// AddUser adds a user with a password.
func (k *Keycloak) AddUser(username, email, password string) {
	k.admin(http.MethodPost, "/admin/realms/test/users", map[string]any{"username": username, "email": email, "emailVerified": true,
		"enabled": true, "firstName": username, "lastName": "Test",
		"credentials": []any{map[string]any{"type": "password", "value": password, "temporary": false}}})
}

// AddSAMLClient registers an SP (entity id, ACS) that gets the user name
// and email as attributes, signed responses, the POST binding.
func (k *Keycloak) AddSAMLClient(entityID, acs string) {
	mapper := func(name, property string) map[string]any {
		return map[string]any{"name": name, "protocol": "saml", "protocolMapper": "saml-user-property-mapper",
			"config": map[string]any{"user.attribute": property, "attribute.name": name, "attribute.nameformat": "Basic"}}
	}
	k.admin(http.MethodPost, "/admin/realms/test/clients", map[string]any{"clientId": entityID, "protocol": "saml", "enabled": true,
		"redirectUris": []string{acs}, "attributes": map[string]any{
			"saml_assertion_consumer_url_post": acs, "saml.client.signature": "false", "saml.force.post.binding": "true",
			"saml.server.signature": "true", "saml.assertion.signature": "true", "saml_name_id_format": "username", "saml.authnstatement": "true"},
		"protocolMappers": []any{mapper("email", "email"), mapper("username", "username")}})
}

// LoginForm finds Keycloak's login form in page: where it posts.
func LoginForm(page []byte) (string, bool) {
	s := string(page)
	i := strings.Index(s, `id="kc-form-login"`)
	if i < 0 {
		return "", false
	}
	j := strings.Index(s[i:], `action="`)
	if j < 0 {
		return "", false
	}
	rest := s[i+j+len(`action="`):]
	end := strings.Index(rest, `"`)
	return strings.ReplaceAll(rest[:end], "&amp;", "&"), true
}
