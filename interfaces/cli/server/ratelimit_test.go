package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withLimits(c *config.Config) {
	c.RateLimit.Enabled = true
	c.RateLimit.LoginIP = "3/15m"
	c.RateLimit.LoginAccount = "5/15m"
	c.RateLimit.RegisterIP = "2/1h"
	c.RateLimit.APIUser = "8/1m"
	c.RateLimit.APIAnonymous = "4/1m"
	c.RateLimit.GitUser = "3/1m"
	c.RateLimit.GitAnonymous = "2/1m"
}

// do sends a JSON request with optional headers and returns status, body
// and headers.
func (s *testServer) do(t *testing.T, method, path string, headers map[string]string, body any) (int, map[string]any, http.Header) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, s.url+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func TestLoginLimits(t *testing.T) {
	srv := startTestServer(t, withLimits, func(c *config.Config) { c.Server.TrustedProxies = []string{"127.0.0.1"} })
	srv.registerAndLogin(t, "alice@example.com", "alice")
	login := func(email, password, ip string) (int, map[string]any, http.Header) {
		return srv.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"X-Forwarded-For": ip}, map[string]any{"email": email, "password": password})
	}
	for range 4 {
		status, _, _ := login("alice@example.com", "Correct-Horse-Battery-9", "1.1.1.1")
		require.Equal(t, http.StatusOK, status, "successful logins don't count")
	}
	for range 3 {
		status, _, _ := login("alice@example.com", "wrong-password-1", "2.2.2.2")
		require.Equal(t, http.StatusUnauthorized, status)
	}
	status, body, header := login("alice@example.com", "Correct-Horse-Battery-9", "2.2.2.2")
	assert.Equal(t, http.StatusTooManyRequests, status, "this IP is out of attempts, even with the right password")
	assert.Equal(t, "RATE_LIMITED", body["type"])
	assert.NotEmpty(t, header.Get("Retry-After"))
	status, _, _ = login("nobody@example.com", "x", "2.2.2.2")
	assert.Equal(t, http.StatusTooManyRequests, status, "any account from that IP")
	status, _, _ = login("alice@example.com", "Correct-Horse-Battery-9", "3.3.3.3")
	assert.Equal(t, http.StatusOK, status, "other IPs still log in")

	// The account limit spans IPs: 3 failures above + 2 more reach 5.
	for _, ip := range []string{"4.4.4.4", "5.5.5.5"} {
		status, _, _ := login("alice@example.com", "wrong-password-1", ip)
		require.Equal(t, http.StatusUnauthorized, status)
	}
	status, _, _ = login("alice@example.com", "Correct-Horse-Battery-9", "6.6.6.6")
	assert.Equal(t, http.StatusTooManyRequests, status, "the account is out of attempts from any IP")
	status, _, _ = login("ALICE@example.com", "Correct-Horse-Battery-9", "7.7.7.7")
	assert.Equal(t, http.StatusTooManyRequests, status, "whatever the case")

	// The web UI's login shares the limits.
	status, _, _ = srv.do(t, http.MethodPost, "/api/v1/auth/session", map[string]string{"X-Forwarded-For": "2.2.2.2"}, map[string]any{"email": "x@example.com", "password": "y"})
	assert.Equal(t, http.StatusTooManyRequests, status)
}

func TestForwardedForIsIgnoredWithoutTrustedProxies(t *testing.T) {
	srv := startTestServer(t, withLimits)
	for i := range 3 {
		status, _, _ := srv.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"X-Forwarded-For": fmt.Sprintf("9.9.9.%d", i)}, map[string]any{"email": "a@example.com", "password": "x"})
		require.Equal(t, http.StatusUnauthorized, status)
	}
	status, _, _ := srv.do(t, http.MethodPost, "/api/v1/auth/login", map[string]string{"X-Forwarded-For": "9.9.9.99"}, map[string]any{"email": "a@example.com", "password": "x"})
	assert.Equal(t, http.StatusTooManyRequests, status, "a spoofed header doesn't buy new attempts")
}

func TestRegistrationAndPasswordLimits(t *testing.T) {
	srv := startTestServer(t, withLimits)
	register := func(n int) int {
		status, _ := srv.post(t, "/api/v1/auth/register", "", map[string]any{"email": fmt.Sprintf("u%d@example.com", n), "username": fmt.Sprintf("u%d", n), "password": "Correct-Horse-Battery-9"})
		return status
	}
	assert.Equal(t, http.StatusOK, register(1))
	status, _ := srv.post(t, "/api/v1/auth/verify/resend", "", map[string]any{"email": "u1@example.com"})
	assert.Equal(t, http.StatusOK, status, "resends share the budget")
	assert.Equal(t, http.StatusTooManyRequests, register(2))

	srv2 := startTestServer(t, withLimits)
	session := srv2.registerAndLogin(t, "p@example.com", "p")
	for range 3 {
		status, _ := srv2.post(t, "/api/v1/user/password", session, map[string]any{"current_password": "wrong", "new_password": "Another-Horse-Battery-9"})
		require.Equal(t, http.StatusBadRequest, status)
	}
	status, body := srv2.post(t, "/api/v1/user/password", session, map[string]any{"current_password": "Correct-Horse-Battery-9", "new_password": "Another-Horse-Battery-9"})
	assert.Equal(t, http.StatusTooManyRequests, status, "a stolen session can't guess the password: %v", body)
}

func TestAPILimits(t *testing.T) {
	srv := startTestServer(t, withLimits)
	session := srv.registerAndLogin(t, "a@example.com", "a")
	// registerAndLogin used 1 anonymous call (register) + login (the auth
	// endpoints count as API calls too): 2 of the anonymous 4.
	for range 2 {
		status, _, h := srv.do(t, http.MethodGet, "/api/v1/explore", nil, nil)
		require.Equal(t, http.StatusOK, status)
		assert.NotEmpty(t, h.Get("RateLimit-Remaining"))
	}
	status, body, h := srv.do(t, http.MethodGet, "/api/v1/explore", nil, nil)
	assert.Equal(t, http.StatusTooManyRequests, status, "anonymous budget used")
	assert.Equal(t, "RATE_LIMITED", body["type"])
	assert.NotEmpty(t, h.Get("Retry-After"))
	status, _, _ = srv.do(t, http.MethodGet, "/api/v1/explore", map[string]string{"Authorization": "Bearer forged.token.value"}, nil)
	assert.Equal(t, http.StatusTooManyRequests, status, "a forged token is anonymous")

	for i := range 8 {
		status, _ := srv.get(t, "/api/v1/user/profile", session)
		require.Equal(t, http.StatusOK, status, "user request %d", i+1)
	}
	status, _ = srv.get(t, "/api/v1/user/profile", session)
	assert.Equal(t, http.StatusTooManyRequests, status, "the user's budget is separate and used up now")
}

func TestGitLimits(t *testing.T) {
	srv := startTestServer(t, withLimits)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "Pub", false)
	refs := srv.url + "/owner/pub.git/info/refs?service=git-upload-pack"
	auth := func(name, token string) map[string]string {
		return basic(&githttp.BasicAuth{Username: name, Password: token})
	}

	for i := range 2 {
		status, _, _ := fetch(t, refs, nil)
		require.Equal(t, http.StatusOK, status, "anonymous %d", i+1)
	}
	status, h, _ := fetch(t, refs, nil)
	assert.Equal(t, http.StatusTooManyRequests, status)
	assert.NotEmpty(t, h.Get("Retry-After"))
	status, _, _ = fetch(t, srv.url+"/owner/pub/raw/main/README.md", nil)
	assert.Equal(t, http.StatusTooManyRequests, status, "raw files count as git operations")

	for i := range 3 {
		status, _, _ := fetch(t, refs, auth(owner.git.Username, owner.git.Password))
		require.Equal(t, http.StatusOK, status, "with a token %d", i+1)
	}
	status, _, _ = fetch(t, refs, auth(owner.git.Username, owner.git.Password))
	assert.Equal(t, http.StatusTooManyRequests, status, "per token")

	srv2 := startTestServer(t, withLimits)
	o2 := srv2.newUser(t, "owner")
	srv2.createRepo(t, o2.session, "Priv", true)
	priv := srv2.url + "/owner/priv.git/info/refs?service=git-upload-pack"
	for range 3 {
		status, _, _ := fetch(t, priv, auth("owner-git", "ogit_wrong"))
		require.Equal(t, http.StatusUnauthorized, status)
	}
	status, _, _ = fetch(t, priv, auth(o2.git.Username, o2.git.Password))
	assert.Equal(t, http.StatusTooManyRequests, status, "token guessing locks the IP out")
}
