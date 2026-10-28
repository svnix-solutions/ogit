package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browser is an HTTP client with a cookie jar, like the web UI.
type browser struct {
	t    *testing.T
	srv  *testServer
	http *http.Client
}

func newBrowser(t *testing.T, srv *testServer) *browser {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	return &browser{t: t, srv: srv, http: &http.Client{Jar: jar}}
}

func (b *browser) cookie(name string) *http.Cookie {
	u, _ := url.Parse(b.srv.url)
	for _, c := range b.http.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// do sends a JSON request; csrf adds the X-CSRF-Token header from the
// cookie, as the UI does.
func (b *browser) do(method, path string, body any, csrf bool) (int, map[string]any, http.Header) {
	b.t.Helper()
	var r io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(b.t, err)
		r = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, b.srv.url+path, r)
	require.NoError(b.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c := b.cookie(websession.CSRFCookie); csrf && c != nil {
		req.Header.Set(websession.CSRFHeader, c.Value)
	}
	resp, err := b.http.Do(req)
	require.NoError(b.t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (b *browser) login(email string) {
	b.t.Helper()
	status, body, _ := b.do(http.MethodPost, "/api/v1/auth/session", map[string]any{"email": email, "password": "Correct-Horse-Battery-9"}, false)
	require.Equal(b.t, http.StatusOK, status, "%v", body)
}

func TestWebSession(t *testing.T) {
	srv := startTestServer(t)
	srv.registerAndLogin(t, "web@example.com", "web")
	b := newBrowser(t, srv)

	status, body, header := b.do(http.MethodPost, "/api/v1/auth/session", map[string]any{"email": "web@example.com", "password": "Correct-Horse-Battery-9"}, false)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.NotContains(t, body, "token", "the token is not readable by page scripts")
	assert.Contains(t, body, "expires_at")
	var sessionHeader string
	for _, h := range header.Values("Set-Cookie") {
		if strings.HasPrefix(h, websession.SessionCookie+"=") {
			sessionHeader = h
		}
	}
	assert.Contains(t, sessionHeader, "HttpOnly")
	assert.Contains(t, sessionHeader, "SameSite=Lax")
	assert.NotContains(t, sessionHeader, "Secure", "plain HTTP test server")
	csrf := b.cookie(websession.CSRFCookie)
	require.NotNil(t, csrf)
	assert.Len(t, csrf.Value, 64)

	status, body, _ = b.do(http.MethodGet, "/api/v1/user/profile", nil, false)
	assert.Equal(t, http.StatusOK, status, "reads need no CSRF token: %v", body)
	assert.Equal(t, "web", body["username"])

	status, body, _ = b.do(http.MethodPost, "/api/v1/repository", map[string]any{"name": "Web Repo"}, false)
	assert.Equal(t, http.StatusForbidden, status, "writes without the CSRF header are refused")
	assert.Equal(t, "CSRF_FAILED", body["type"])

	req, _ := http.NewRequest(http.MethodPost, srv.url+"/api/v1/repository", strings.NewReader(`{"name":"Web Repo"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(websession.CSRFHeader, strings.Repeat("0", 64))
	resp, err := b.http.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "a guessed CSRF token is refused")

	status, body, _ = b.do(http.MethodPost, "/api/v1/repository", map[string]any{"name": "Web Repo"}, true)
	assert.Equal(t, http.StatusOK, status, "%v", body)

	// A stale cookie does not stop logging in again.
	b.login("web@example.com")

	status, _, _ = b.do(http.MethodDelete, "/api/v1/auth/session", nil, false)
	assert.Equal(t, http.StatusForbidden, status, "logout is a write too")
	old := b.cookie(websession.SessionCookie).Value
	status, body, _ = b.do(http.MethodDelete, "/api/v1/auth/session", nil, true)
	assert.Equal(t, http.StatusOK, status, "%v", body)
	assert.Nil(t, b.cookie(websession.SessionCookie), "cookies are deleted")
	assert.Nil(t, b.cookie(websession.CSRFCookie))
	status, _ = srv.get(t, "/api/v1/user/profile", old)
	assert.NotEqual(t, http.StatusOK, status, "the session is over, not just forgotten by the browser")
}

func TestWebSessionLoginErrors(t *testing.T) {
	srv := startTestServer(t)
	srv.registerAndLogin(t, "web@example.com", "web")
	b := newBrowser(t, srv)
	status, body, _ := b.do(http.MethodPost, "/api/v1/auth/session", map[string]any{"email": "web@example.com", "password": "wrong-password-123"}, false)
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.Equal(t, "INVALID_CREDENTIALS", body["type"], "same answers as /auth/login")
	assert.Nil(t, b.cookie(websession.SessionCookie))
}

// Bearer tokens are not affected: no CSRF token needed, and a session
// cookie next to one is ignored.
func TestBearerIgnoresCookies(t *testing.T) {
	srv := startTestServer(t)
	bearer := srv.registerAndLogin(t, "api@example.com", "apiuser")
	srv.registerAndLogin(t, "web@example.com", "web")
	b := newBrowser(t, srv)
	b.login("web@example.com")

	req, _ := http.NewRequest(http.MethodGet, srv.url+"/api/v1/user/profile", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := b.http.Do(req) // sends web's cookies too
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "apiuser", body["username"])

	status, _ := srv.post(t, "/api/v1/repository", bearer, map[string]any{"name": "Api Repo"})
	assert.Equal(t, http.StatusOK, status)
}

func TestWebUIRoutes(t *testing.T) {
	srv := startTestServer(t)
	get := func(path string) (int, string, http.Header) {
		resp, err := http.Get(srv.url + path)
		require.NoError(t, err)
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data), resp.Header
	}

	status, body, header := get("/some/app/route")
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, header.Get("Content-Type"), "text/html")
	assert.Contains(t, header.Get("Content-Security-Policy"), "default-src 'self'")
	assert.NotEmpty(t, body)

	status, body, _ = get("/api/v1/does-not-exist")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Contains(t, body, `"NOT_FOUND"`, "API paths stay JSON")

	status, _, _ = get("/api/health")
	assert.Equal(t, http.StatusOK, status, "real routes win")
	status, _, _ = get("/documentation")
	assert.Equal(t, http.StatusOK, status)
}
