package server

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// idpUser is who signs in at the fake providers.
type idpUser struct {
	sub, email, username string
	verified             bool
	orgs                 []string // GitHub
	groups               []string // OIDC: in "groups" and "realm_access.roles"
}

type issued struct {
	user               idpUser
	nonce, challenge   string
	redirect, clientId string
}

// fakeIdP is an OpenID Connect provider (discovery, JWKS, authorize,
// token with PKCE, userinfo) signing real RS256 ID tokens; with github it
// is GitHub's OAuth2 and API instead.
type fakeIdP struct {
	t        *testing.T
	srv      *httptest.Server
	github   bool
	key      *rsa.PrivateKey
	signer   jose.Signer
	clientId string
	secret   string

	mu        sync.Mutex
	next      idpUser
	deny      bool // the user refuses at the provider
	badNonce  bool // the ID token carries another nonce
	codes     map[string]issued
	tokens    map[string]idpUser
	lastQuery url.Values
}

func startFakeIdP(t *testing.T, github bool) *fakeIdP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, (&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	f := &fakeIdP{t: t, github: github, key: key, signer: signer, clientId: "ogit-client", secret: "ogit-secret", codes: map[string]issued{}, tokens: map[string]idpUser{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) signInAs(u idpUser) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next, f.deny, f.badNonce = u, false, false
}

func random() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (f *fakeIdP) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(map[string]any{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"jwks_uri": f.srv.URL + "/jwks", "userinfo_endpoint": f.srv.URL + "/userinfo", "id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}})
	case "/jwks":
		writeJSON(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &f.key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	case "/authorize", "/login/oauth/authorize":
		q := r.URL.Query()
		f.lastQuery = q
		back, _ := url.Parse(q.Get("redirect_uri"))
		bq := back.Query()
		bq.Set("state", q.Get("state"))
		if f.deny {
			bq.Set("error", "access_denied")
		} else {
			code := random()
			f.codes[code] = issued{user: f.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), clientId: q.Get("client_id")}
			bq.Set("code", code)
		}
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	case "/token", "/login/oauth/access_token":
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		is, found := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case id != f.clientId || secret != f.secret:
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		case !found || is.redirect != r.Form.Get("redirect_uri") || is.clientId != id:
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		case base64.RawURLEncoding.EncodeToString(sum[:]) != is.challenge:
			http.Error(w, `{"error":"invalid_grant","error_description":"PKCE"}`, http.StatusBadRequest)
			return
		}
		access := random()
		f.tokens[access] = is.user
		out := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 3600}
		if !f.github {
			nonce := is.nonce
			if f.badNonce {
				nonce = "other"
			}
			claims := map[string]any{"iss": f.srv.URL, "aud": f.clientId, "sub": is.user.sub, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(),
				"nonce": nonce, "email": is.user.email, "email_verified": is.user.verified, "preferred_username": is.user.username,
				"groups": is.user.groups, "realm_access": map[string]any{"roles": is.user.groups}}
			raw, err := jwt.Signed(f.signer).Claims(claims).Serialize()
			require.NoError(f.t, err)
			out["id_token"] = raw
		}
		writeJSON(out)
	default:
		u, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/userinfo":
			writeJSON(map[string]any{"sub": u.sub, "email": u.email, "email_verified": u.verified, "preferred_username": u.username})
		case "/api/v3/user":
			n, _ := strconv.Atoi(u.sub)
			writeJSON(map[string]any{"id": n, "login": u.username, "name": u.username})
		case "/api/v3/user/emails":
			writeJSON([]any{map[string]any{"email": "old@example.net", "primary": false, "verified": true},
				map[string]any{"email": u.email, "primary": true, "verified": u.verified}})
		case "/api/v3/user/orgs":
			var orgs []any
			for _, o := range u.orgs {
				orgs = append(orgs, map[string]any{"login": o})
			}
			writeJSON(orgs)
		default:
			http.NotFound(w, r)
		}
	}
}

// ssoProviders configures the test server's providers (as the
// configuration file would) and its public URL.
func (s *testServer) ssoProviders(t *testing.T, ps ...config.SSOProviderConfig) {
	t.Helper()
	s.deps.SSOApp.Config.Server.PublicURL = s.url
	require.NoError(t, s.deps.SSOApp.LoadConfig(ps))
}

func oidcProvider(f *fakeIdP, slug string, edit ...func(*config.SSOProviderConfig)) config.SSOProviderConfig {
	p := config.SSOProviderConfig{Slug: slug, Name: "Corp " + slug, Kind: "oidc", Issuer: f.srv.URL, ClientId: f.clientId, ClientSecret: f.secret}
	for _, e := range edit {
		e(&p)
	}
	return p
}

// sso drives the browser through a sign-in: from the start URL, through
// the provider and the callback, to where oGit sends it at the end.
func (b *browser) sso(start string) *url.URL {
	b.t.Helper()
	client := &http.Client{Jar: b.http.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	next := b.srv.url + start
	for range 10 {
		req, err := http.NewRequest(http.MethodGet, next, nil)
		require.NoError(b.t, err)
		resp, err := client.Do(req)
		require.NoError(b.t, err)
		resp.Body.Close()
		require.Equal(b.t, http.StatusFound, resp.StatusCode, "GET %s", next)
		loc, err := req.URL.Parse(resp.Header.Get("Location"))
		require.NoError(b.t, err)
		if loc.Host == req.URL.Host && !strings.HasPrefix(loc.Path, "/api/v1/auth/sso/") && !strings.HasPrefix(loc.Path, "/authorize") {
			return loc
		}
		if loc.Host != strings.TrimPrefix(b.srv.url, "http://") || strings.HasPrefix(loc.Path, "/api/v1/auth/sso/") {
			next = loc.String()
			continue
		}
		return loc
	}
	b.t.Fatal("too many redirects")
	return nil
}

func (b *browser) profile() (int, map[string]any) {
	status, body, _ := b.do(http.MethodGet, "/api/v1/user/profile", nil, false)
	return status, body
}

func TestSSOWithOIDC(t *testing.T) {
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "corp"))
	alice := srv.newUser(t, "alice")

	status, providers := srv.get(t, "/api/v1/auth/providers", "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []any{map[string]any{"slug": "corp", "name": "Corp corp", "kind": "oidc"}}, providers["providers"])

	t.Run("a new account", func(t *testing.T) {
		idp.signInAs(idpUser{sub: "u-1", email: "carol@corp.example", username: "Carol.Doe", verified: true})
		b := newBrowser(t, srv)
		loc := b.sso("/api/v1/auth/sso/corp/start?redirect=/explore")
		assert.Equal(t, "/explore", loc.RequestURI(), "%v", loc)
		assert.NotEmpty(t, idp.lastQuery.Get("code_challenge"), "PKCE")
		assert.Equal(t, "S256", idp.lastQuery.Get("code_challenge_method"))
		assert.NotEmpty(t, idp.lastQuery.Get("nonce"))
		status, me := b.profile()
		require.Equal(t, http.StatusOK, status, "%v", me)
		assert.Equal(t, "carol-doe", me["username"], "made a valid user name")
		assert.Equal(t, "carol@corp.example", me["email"])
		assert.Equal(t, false, me["has_password"])

		again := newBrowser(t, srv)
		again.sso("/api/v1/auth/sso/corp/start")
		_, me2 := again.profile()
		assert.Equal(t, me["id"], me2["id"], "the same account next time")
	})

	t.Run("name clashes", func(t *testing.T) {
		idp.signInAs(idpUser{sub: "u-2", email: "other-alice@corp.example", username: "alice", verified: true})
		b := newBrowser(t, srv)
		b.sso("/api/v1/auth/sso/corp/start")
		_, me := b.profile()
		assert.Equal(t, "alice-2", me["username"])
		idp.signInAs(idpUser{sub: "u-3", email: "adm@corp.example", username: "admin", verified: true})
		b = newBrowser(t, srv)
		b.sso("/api/v1/auth/sso/corp/start")
		_, me = b.profile()
		assert.Equal(t, "user-admin", me["username"], "a reserved name")
	})

	t.Run("linking an existing account by email", func(t *testing.T) {
		idp.signInAs(idpUser{sub: "u-alice", email: "alice@example.com", username: "ally", verified: true})
		b := newBrowser(t, srv)
		loc := b.sso("/api/v1/auth/sso/corp/start?redirect=/alice")
		require.Equal(t, "/login/link", loc.Path, "%v", loc)
		token := strings.TrimPrefix(loc.Fragment, "token=")
		require.NotEmpty(t, token)
		status, _ := b.profile()
		assert.NotEqual(t, http.StatusOK, status, "not signed in yet")

		status, info, _ := b.do(http.MethodGet, "/api/v1/auth/sso/link?token="+token, nil, false)
		require.Equal(t, http.StatusOK, status, "%v", info)
		assert.Equal(t, "alice", info["username"])
		status, body, _ := b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": "wrong-password-1A"}, false)
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		status, body, _ = b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "/alice", body["redirect"])
		_, me := b.profile()
		assert.Equal(t, "alice", me["username"])
		status, _, _ = b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		assert.Equal(t, http.StatusNotFound, status, "once")

		b = newBrowser(t, srv)
		assert.Equal(t, "/", b.sso("/api/v1/auth/sso/corp/start").Path, "linked: straight in")
		_, me = b.profile()
		assert.Equal(t, "alice", me["username"])
		_, ids, _ := b.do(http.MethodGet, "/api/v1/user/identities", nil, false)
		require.Len(t, ids["identities"], 1)
		assert.Equal(t, "Corp corp", ids["identities"].([]any)[0].(map[string]any)["provider_name"])
	})

	t.Run("refusals", func(t *testing.T) {
		fail := func(loc *url.URL) string {
			t.Helper()
			assert.Equal(t, "/login", loc.Path, "%v", loc)
			return loc.Query().Get("sso_error")
		}
		idp.signInAs(idpUser{sub: "u-4", email: "dave@corp.example", username: "dave", verified: false})
		assert.Contains(t, fail(newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")), "verified email")

		idp.signInAs(idpUser{sub: "u-5", email: "alice@example.com", username: "mallory", verified: false})
		b := newBrowser(t, srv)
		assert.Contains(t, fail(b.sso("/api/v1/auth/sso/corp/start")), "verified email", "an unverified email links nothing")

		idp.signInAs(idpUser{sub: "u-6", email: "erin@corp.example", username: "erin", verified: true})
		idp.mu.Lock()
		idp.deny = true
		idp.mu.Unlock()
		assert.Contains(t, fail(newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")), "access_denied")

		idp.signInAs(idpUser{sub: "u-6", email: "erin@corp.example", username: "erin", verified: true})
		idp.mu.Lock()
		idp.badNonce = true
		idp.mu.Unlock()
		assert.Contains(t, fail(newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")), "failed")

		// Another browser can't finish the flow, nor can it be replayed.
		idp.signInAs(idpUser{sub: "u-6", email: "erin@corp.example", username: "erin", verified: true})
		first := newBrowser(t, srv)
		client := &http.Client{Jar: first.http.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Get(srv.url + "/api/v1/auth/sso/corp/start")
		require.NoError(t, err)
		resp.Body.Close()
		resp, err = client.Get(resp.Header.Get("Location")) // the provider
		require.NoError(t, err)
		resp.Body.Close()
		callback := resp.Header.Get("Location")
		other := newBrowser(t, srv)
		otherClient := &http.Client{Jar: other.http.Jar, CheckRedirect: client.CheckRedirect}
		resp, err = otherClient.Get(callback)
		require.NoError(t, err)
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		assert.Contains(t, loc.Query().Get("sso_error"), "another browser")
		resp, err = client.Get(callback)
		require.NoError(t, err)
		resp.Body.Close()
		loc, _ = url.Parse(resp.Header.Get("Location"))
		assert.Contains(t, loc.Query().Get("sso_error"), "expired", "the other browser's attempt used it up")

		// Redirects stay here.
		b = newBrowser(t, srv)
		assert.Equal(t, "/", b.sso("/api/v1/auth/sso/corp/start?redirect=//evil.example/x").RequestURI())

		// Disabled accounts.
		idp.signInAs(idpUser{sub: "u-1", email: "carol@corp.example", username: "Carol.Doe", verified: true})
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "carol-doe").Update("active", false).Error)
		assert.Contains(t, fail(newBrowser(t, srv).sso("/api/v1/auth/sso/corp/start")), "disabled")
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "carol-doe").Update("active", true).Error)
	})

	t.Run("allowed domains and no auto-creation", func(t *testing.T) {
		srv.ssoProviders(t, oidcProvider(idp, "corp"), oidcProvider(idp, "strict", func(p *config.SSOProviderConfig) {
			p.AllowedDomains, p.AutoCreate = "partner.example", new(bool)
		}))
		idp.signInAs(idpUser{sub: "s-1", email: "x@corp.example", username: "x", verified: true})
		loc := newBrowser(t, srv).sso("/api/v1/auth/sso/strict/start")
		assert.Contains(t, loc.Query().Get("sso_error"), "isn't allowed")
		idp.signInAs(idpUser{sub: "s-2", email: "y@partner.example", username: "y", verified: true})
		loc = newBrowser(t, srv).sso("/api/v1/auth/sso/strict/start")
		assert.Contains(t, loc.Query().Get("sso_error"), "no account")
		srv.ssoProviders(t, oidcProvider(idp, "corp"))
	})

	_ = alice
}

func TestSSOTwoFactor(t *testing.T) {
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "trusted"), oidcProvider(idp, "strict", func(p *config.SSOProviderConfig) { p.RequireTwoFactor = true }))
	alice := srv.newUser(t, "alice")
	_, recovery := srv.enableTOTP(t, alice)

	// Link alice to both providers (password + a 2FA code).
	for i, slug := range []string{"trusted", "strict"} {
		idp.signInAs(idpUser{sub: "alice-" + slug, email: "alice@example.com", username: "alice", verified: true})
		b := newBrowser(t, srv)
		token := strings.TrimPrefix(b.sso("/api/v1/auth/sso/"+slug+"/start").Fragment, "token=")
		status, body, _ := b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
		require.Equal(t, http.StatusUnauthorized, status, "%v", body)
		assert.Equal(t, "TWO_FACTOR_CODE_REQUIRED", body["type"])
		c := recovery[i] // the current TOTP step went into enableTOTP
		status, body, _ = b.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword, "code": c}, false)
		require.Equal(t, http.StatusOK, status, "%v", body)
	}

	idp.signInAs(idpUser{sub: "alice-trusted", email: "alice@example.com", username: "alice", verified: true})
	b := newBrowser(t, srv)
	assert.Equal(t, "/", b.sso("/api/v1/auth/sso/trusted/start").Path, "the provider's MFA is trusted: no code")

	idp.signInAs(idpUser{sub: "alice-strict", email: "alice@example.com", username: "alice", verified: true})
	loc := newBrowser(t, srv).sso("/api/v1/auth/sso/strict/start?redirect=/x")
	assert.Equal(t, "/login", loc.Path)
	assert.NotEmpty(t, loc.Query().Get("challenge"), "oGit 2FA is asked: %v", loc)
	assert.Equal(t, "/x", loc.Query().Get("redirect"))

	// Site-wide 2FA: a passwordless SSO account counts as having it; a
	// password account only in sessions opened through a trusted provider.
	carol := srv.newUser(t, "carol")
	srv.deps.TwoFactorApp.Config.Security.RequireTwoFactor = true
	idp.signInAs(idpUser{sub: "bob-1", email: "bob@corp.example", username: "bob", verified: true})
	bob := newBrowser(t, srv)
	bob.sso("/api/v1/auth/sso/trusted/start")
	status, me := bob.profile()
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, me["two_factor_required"])
	status, _, _ = bob.do(http.MethodGet, "/api/v1/repository", nil, false)
	assert.Equal(t, http.StatusOK, status, "the API is open")

	idp.signInAs(idpUser{sub: "carol-1", email: "carol@example.com", username: "carol", verified: true})
	cb := newBrowser(t, srv)
	token := strings.TrimPrefix(cb.sso("/api/v1/auth/sso/trusted/start").Fragment, "token=")
	status, body, _ := cb.do(http.MethodPost, "/api/v1/auth/sso/link", map[string]any{"token": token, "password": testPassword}, false)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _, _ = cb.do(http.MethodGet, "/api/v1/repository", nil, false)
	assert.Equal(t, http.StatusOK, status, "a session through a trusted provider")
	status, _ = srv.get(t, "/api/v1/repository", carol.session)
	assert.Equal(t, http.StatusForbidden, status, "a password session still needs oGit 2FA")
}

func TestSSOAccountSettings(t *testing.T) {
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "corp"), oidcProvider(idp, "other"))
	idp.signInAs(idpUser{sub: "d-1", email: "dana@corp.example", username: "dana", verified: true})
	b := newBrowser(t, srv)
	b.sso("/api/v1/auth/sso/corp/start")
	_, ids, _ := b.do(http.MethodGet, "/api/v1/user/identities", nil, false)
	id := ids["identities"].([]any)[0].(map[string]any)["id"].(string)

	status, body, _ := b.do(http.MethodDelete, "/api/v1/user/identities/"+id, nil, true)
	assert.Equal(t, http.StatusConflict, status, "the only way to sign in: %v", body)
	assert.Equal(t, "LAST_SIGN_IN_METHOD", body["type"])

	// Linking another provider from the settings: with sudo.
	idp.signInAs(idpUser{sub: "d-2", email: "dana@other.example", username: "dana2", verified: true})
	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
	loc := b.sso("/api/v1/auth/sso/other/start?purpose=link&redirect=/settings")
	assert.Contains(t, loc.Query().Get("sso_error"), "confirm it's you", "%v", loc)
	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(time.Minute)).Error)
	loc = b.sso("/api/v1/auth/sso/other/start?purpose=link&redirect=/settings")
	assert.Equal(t, "/settings", loc.Path, "%v", loc)
	_, ids, _ = b.do(http.MethodGet, "/api/v1/user/identities", nil, false)
	assert.Len(t, ids["identities"], 2)
	status, _, _ = b.do(http.MethodDelete, "/api/v1/user/identities/"+id, nil, true)
	assert.Equal(t, http.StatusOK, status, "another way remains")

	// An identity linked to someone else.
	idp.signInAs(idpUser{sub: "e-1", email: "eve@corp.example", username: "eve", verified: true})
	eve := newBrowser(t, srv)
	eve.sso("/api/v1/auth/sso/corp/start")
	idp.signInAs(idpUser{sub: "d-2", email: "dana@other.example", username: "dana2", verified: true})
	loc = eve.sso("/api/v1/auth/sso/other/start?purpose=link&redirect=/settings")
	assert.Contains(t, loc.Query().Get("sso_error"), "linked to another user")

	// Sudo through the provider, then a first password.
	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
	status, body, _ = b.do(http.MethodPost, "/api/v1/user/password", map[string]any{"new_password": testPassword}, true)
	require.Equal(t, http.StatusForbidden, status, "sudo first: %v", body)
	idp.signInAs(idpUser{sub: "e-1", email: "eve@corp.example", username: "eve", verified: true})
	loc = b.sso("/api/v1/auth/sso/corp/start?purpose=sudo&redirect=/settings")
	assert.Contains(t, loc.Query().Get("sso_error"), "isn't allowed", "someone else's account confirms nothing")
	idp.signInAs(idpUser{sub: "d-2", email: "dana@other.example", username: "dana2", verified: true})
	loc = b.sso("/api/v1/auth/sso/other/start?purpose=sudo&redirect=/settings")
	assert.Equal(t, "/settings", loc.RequestURI(), "%v", loc)
	status, body, _ = b.do(http.MethodPost, "/api/v1/user/password", map[string]any{"new_password": testPassword}, true)
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, me := b.profile()
	assert.Equal(t, true, me["has_password"])
	status, _ = srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "dana@corp.example", "password": testPassword})
	assert.Equal(t, http.StatusOK, status, "the password works")
}

func TestSSOWithGitHub(t *testing.T) {
	srv := startTestServer(t)
	gh := startFakeIdP(t, true)
	srv.ssoProviders(t, config.SSOProviderConfig{Slug: "github", Name: "GitHub", Kind: "github", Issuer: gh.srv.URL, ClientId: gh.clientId,
		ClientSecret: gh.secret, AllowedOrgs: "acme"})
	gh.signInAs(idpUser{sub: "4242", email: "octo@acme.example", username: "octocat", verified: true, orgs: []string{"other", "ACME"}})
	b := newBrowser(t, srv)
	assert.Equal(t, "/", b.sso("/api/v1/auth/sso/github/start").Path)
	_, me := b.profile()
	assert.Equal(t, "octocat", me["username"])
	assert.Equal(t, "octo@acme.example", me["email"], "the primary verified email")
	assert.Contains(t, gh.lastQuery.Get("scope"), "read:org")

	gh.signInAs(idpUser{sub: "4343", email: "x@else.example", username: "outsider", verified: true, orgs: []string{"other"}})
	loc := newBrowser(t, srv).sso("/api/v1/auth/sso/github/start")
	assert.Contains(t, loc.Query().Get("sso_error"), "isn't allowed", "not in the organization")
}

func TestSSOAdminProviders(t *testing.T) {
	srv := startTestServer(t)
	idp := startFakeIdP(t, false)
	srv.ssoProviders(t, oidcProvider(idp, "fromfile"))
	admin, plain := srv.newUser(t, "root"), srv.newUser(t, "plain")
	srv.promote(t, "root")
	provider := map[string]any{"slug": "keycloak", "name": "Keycloak", "kind": "oidc", "issuer": idp.srv.URL, "client_id": idp.clientId, "client_secret": idp.secret}

	status, _ := srv.post(t, "/api/v1/admin/sso/providers", plain.session, provider)
	assert.Equal(t, http.StatusForbidden, status)
	status, body := srv.post(t, "/api/v1/admin/sso/providers", admin.session, provider)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, srv.url+"/api/v1/auth/sso/keycloak/callback", body["callback_url"])
	assert.Equal(t, true, body["has_secret"])
	assert.NotContains(t, body, "client_secret")
	status, _ = srv.post(t, "/api/v1/admin/sso/providers", admin.session, provider)
	assert.Equal(t, http.StatusConflict, status, "the slug is taken")
	for _, bad := range []map[string]any{
		{"slug": "Bad Slug", "name": "x", "kind": "oidc", "issuer": idp.srv.URL, "client_id": "x", "client_secret": "x"},
		{"slug": "x", "name": "x", "kind": "saml", "issuer": idp.srv.URL, "client_id": "x", "client_secret": "x"},
		{"slug": "x", "name": "x", "kind": "oidc", "issuer": "ftp://x", "client_id": "x", "client_secret": "x"},
		{"slug": "x", "name": "x", "kind": "oidc", "issuer": idp.srv.URL, "client_id": "x"},
	} {
		status, _ = srv.post(t, "/api/v1/admin/sso/providers", admin.session, bad)
		assert.Equal(t, http.StatusBadRequest, status, "%v", bad)
	}

	idp.signInAs(idpUser{sub: "k-1", email: "kim@corp.example", username: "kim", verified: true})
	b := newBrowser(t, srv)
	b.sso("/api/v1/auth/sso/keycloak/start")
	_, me := b.profile()
	assert.Equal(t, "kim", me["username"], "the stored provider works (sealed secret)")

	status, body = srv.request(t, http.MethodPatch, "/api/v1/admin/sso/providers/keycloak", admin.session, map[string]any{"enabled": false})
	require.Equal(t, http.StatusOK, status, "%v", body)
	_, providers := srv.get(t, "/api/v1/auth/providers", "")
	assert.Len(t, providers["providers"], 1, "disabled ones are hidden")
	status, _ = srv.request(t, http.MethodPatch, "/api/v1/admin/sso/providers/fromfile", admin.session, map[string]any{"enabled": false})
	assert.Equal(t, http.StatusConflict, status, "the configuration's are read-only")
	_, list := srv.get(t, "/api/v1/admin/sso/providers", admin.session)
	require.Len(t, list["providers"], 2)

	require.NoError(t, srv.deps.Database.Db.Model(&domain.Session{}).Where("1 = 1").Update("sudo_until", time.Now().Add(-time.Minute)).Error)
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/sso/providers/keycloak", admin.session, nil)
	assert.Equal(t, http.StatusForbidden, status, "sudo")
}
