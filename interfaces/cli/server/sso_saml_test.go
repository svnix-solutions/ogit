package server

import (
	"encoding/base64"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crewjam/saml"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/sso/samltest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	formAction = regexp.MustCompile(`<form[^>]*action="([^"]+)"`)
	formInput  = regexp.MustCompile(`<input[^>]*name="([^"]+)"[^>]*value="([^"]*)"`)
)

// samlForm reads an auto-posting form: its action and fields.
func samlForm(t *testing.T, page []byte) (string, url.Values) {
	t.Helper()
	m := formAction.FindSubmatch(page)
	require.NotNil(t, m, "no form in %s", page)
	fields := url.Values{}
	for _, f := range formInput.FindAllSubmatch(page, -1) {
		fields.Set(html.UnescapeString(string(f[1])), html.UnescapeString(string(f[2])))
	}
	return html.UnescapeString(string(m[1])), fields
}

func (b *browser) noRedirects() *http.Client {
	return &http.Client{Jar: b.http.Jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// postForm posts fields to action and follows our own redirects (finish)
// to where oGit sends the browser in the end.
func (b *browser) postForm(action string, fields url.Values) *url.URL {
	b.t.Helper()
	client := b.noRedirects()
	resp, err := client.PostForm(action, fields)
	require.NoError(b.t, err)
	resp.Body.Close()
	for range 5 {
		require.Contains(b.t, []int{http.StatusFound, http.StatusSeeOther}, resp.StatusCode, "POST %s", action)
		loc, err := resp.Request.URL.Parse(resp.Header.Get("Location"))
		require.NoError(b.t, err)
		if !strings.HasPrefix(loc.Path, "/api/v1/auth/sso/") {
			return loc
		}
		resp, err = client.Get(loc.String())
		require.NoError(b.t, err)
		resp.Body.Close()
	}
	b.t.Fatal("too many redirects")
	return nil
}

// samlResponse starts a sign-in and returns the IdP's answer (the ACS
// URL and its form), without posting it.
func (b *browser) samlResponse(start string) (string, url.Values) {
	b.t.Helper()
	client := b.noRedirects()
	resp, err := client.Get(b.srv.url + start)
	require.NoError(b.t, err)
	resp.Body.Close()
	require.Equal(b.t, http.StatusFound, resp.StatusCode)
	resp, err = client.Get(resp.Header.Get("Location")) // the IdP
	require.NoError(b.t, err)
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(b.t, http.StatusOK, resp.StatusCode, "%s", page)
	return samlForm(b.t, page)
}

func (b *browser) saml(start string) *url.URL {
	b.t.Helper()
	action, fields := b.samlResponse(start)
	return b.postForm(action, fields)
}

func samlUser(id, email, uid string, groups ...string) *saml.Session {
	return &saml.Session{NameID: id, NameIDFormat: string(saml.PersistentNameIDFormat), UserName: uid, UserEmail: email, UserCommonName: uid, Groups: groups}
}

// samlProvider adds a SAML provider through the admin API (so its SP key
// is made), with the IdP's metadata from its URL.
func (s *testServer) samlProvider(t *testing.T, admin user, slug string, idp *samltest.IdP, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"slug": slug, "name": "Corp SAML", "kind": "saml", "groups_claim": "eduPersonAffiliation",
		"admin_group": "ogit-admins", "saml": map[string]any{}}
	if idp != nil {
		body["issuer"] = idp.MetadataURL()
	}
	for k, v := range extra {
		body[k] = v
	}
	status, resp := s.post(t, "/api/v1/admin/sso/providers", admin.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	return resp
}

func TestSAML(t *testing.T) {
	srv := startTestServer(t)
	srv.ssoProviders(t)
	idp := samltest.Start(t)
	root := srv.newUser(t, "root")
	srv.promote(t, "root")
	p := srv.samlProvider(t, root, "corp", idp, nil)
	assert.Equal(t, srv.url+"/api/v1/auth/sso/corp/metadata", p["metadata_url"])
	assert.Equal(t, srv.url+"/api/v1/auth/sso/corp/acs", p["acs_url"])
	assert.Empty(t, p["callback_url"])

	t.Run("the SP's metadata", func(t *testing.T) {
		resp, err := http.Get(srv.url + "/api/v1/auth/sso/corp/metadata")
		require.NoError(t, err)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		xml := string(b)
		assert.Contains(t, xml, `entityID="`+srv.url+`/api/v1/auth/sso/corp/metadata"`)
		assert.Contains(t, xml, srv.url+"/api/v1/auth/sso/corp/acs")
		assert.Contains(t, xml, "X509Certificate")
		assert.Contains(t, xml, `AuthnRequestsSigned="true"`)
	})

	t.Run("a new account, groups synced", func(t *testing.T) {
		idp.SignInAs(samlUser("n-1", "nina@corp.example", "nina", "ogit-admins"))
		b := newBrowser(t, srv)
		action, fields := b.samlResponse("/api/v1/auth/sso/corp/start?redirect=/explore")
		xml, err := base64.StdEncoding.DecodeString(fields.Get("SAMLResponse"))
		require.NoError(t, err)
		assert.Contains(t, string(xml), "EncryptedAssertion", "the IdP encrypts for our certificate")
		assert.Equal(t, srv.url+"/api/v1/auth/sso/corp/acs", action)
		loc := b.postForm(action, fields)
		assert.Equal(t, "/explore", loc.RequestURI(), "%v", loc)
		_, me := b.profile()
		assert.Equal(t, "nina", me["username"])
		assert.Equal(t, "nina@corp.example", me["email"])
		assert.Equal(t, true, me["two_factor_satisfied"], "the IdP's MFA is trusted")
		assert.Equal(t, domain.RoleAdmin, srv.userNamed(t, "nina").Role)

		// The same response again: used.
		loc = newBrowser(t, srv).postForm(action, fields)
		assert.NotEmpty(t, loc.Query().Get("sso_error"), "%v", loc)
	})

	t.Run("refusals", func(t *testing.T) {
		idp.SignInAs(samlUser("o-1", "olga@corp.example", "olga"))
		// Another browser can't finish it.
		action, fields := newBrowser(t, srv).samlResponse("/api/v1/auth/sso/corp/start")
		loc := newBrowser(t, srv).postForm(action, fields)
		assert.Contains(t, loc.Query().Get("sso_error"), "another browser")

		// An answer to another request.
		b := newBrowser(t, srv)
		_, first := b.samlResponse("/api/v1/auth/sso/corp/start")
		_, second := b.samlResponse("/api/v1/auth/sso/corp/start")
		first.Set("RelayState", second.Get("RelayState"))
		loc = b.postForm(action, first)
		assert.Contains(t, loc.Query().Get("sso_error"), "isn't allowed", "InResponseTo must be this flow's request")

		// Tampered with.
		b = newBrowser(t, srv)
		_, fields = b.samlResponse("/api/v1/auth/sso/corp/start")
		xml, _ := base64.StdEncoding.DecodeString(fields.Get("SAMLResponse"))
		tampered := strings.Replace(string(xml), `ID="`, `ID="x`, 1)
		fields.Set("SAMLResponse", base64.StdEncoding.EncodeToString([]byte(tampered)))
		loc = b.postForm(action, fields)
		assert.Contains(t, loc.Query().Get("sso_error"), "isn't allowed")

		// An unsolicited response, not allowed for this provider.
		page := idp.IdPInitiated(srv.url+"/api/v1/auth/sso/corp/metadata", "")
		action, fields = samlForm(t, page)
		loc = newBrowser(t, srv).postForm(action, fields)
		assert.NotEmpty(t, loc.Query().Get("sso_error"), "IdP-initiated is off by default")
	})

	t.Run("IdP-initiated when allowed", func(t *testing.T) {
		srv.samlProvider(t, root, "portal", idp, map[string]any{"saml": map[string]any{"allow_idp_initiated": true}})
		idp.SignInAs(samlUser("p-1", "pia@corp.example", "pia"))
		page := idp.IdPInitiated(srv.url+"/api/v1/auth/sso/portal/metadata", "")
		action, fields := samlForm(t, page)
		b := newBrowser(t, srv)
		loc := b.postForm(action, fields)
		assert.Equal(t, "/", loc.Path, "%v", loc)
		_, me := b.profile()
		assert.Equal(t, "pia", me["username"])
		loc = newBrowser(t, srv).postForm(action, fields)
		assert.Contains(t, loc.Query().Get("sso_error"), "already used", "a replay: %v", loc)
	})

	t.Run("a browser that already has a session (a same-site IdP sends our cookies)", func(t *testing.T) {
		idp.SignInAs(samlUser("n-1", "nina@corp.example", "nina"))
		b := newBrowser(t, srv)
		b.saml("/api/v1/auth/sso/corp/start")
		require.NotNil(t, b.cookie("ogit_session"))
		loc := b.saml("/api/v1/auth/sso/corp/start?redirect=/x")
		assert.Equal(t, "/x", loc.RequestURI(), "the ACS POST needs no CSRF header: %v", loc)
	})

	t.Run("pasted metadata", func(t *testing.T) {
		srv.samlProvider(t, root, "pasted", idp, map[string]any{"issuer": "", "saml": map[string]any{"metadata_xml": idp.MetadataXML()}})
		idp.SignInAs(samlUser("q-1", "quinn@corp.example", "quinn"))
		b := newBrowser(t, srv)
		assert.Equal(t, "/", b.saml("/api/v1/auth/sso/pasted/start").Path)
		_, me := b.profile()
		assert.Equal(t, "quinn", me["username"])
	})

	t.Run("bad settings", func(t *testing.T) {
		for _, bad := range []map[string]any{
			{"slug": "x1", "name": "X", "kind": "saml", "issuer": "", "saml": map[string]any{}},
			{"slug": "x2", "name": "X", "kind": "saml", "issuer": "", "saml": map[string]any{"metadata_xml": "<nope/>"}},
		} {
			status, body := srv.post(t, "/api/v1/admin/sso/providers", root.session, bad)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", bad, body)
		}
	})
}
