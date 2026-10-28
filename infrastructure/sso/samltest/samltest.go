// Package samltest is an in-process SAML identity provider for tests
// (crewjam/saml's): signed (and, when the SP asks, encrypted) responses
// for whoever SignInAs set.
package samltest

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/labbs/ogit/infrastructure/sso"
)

// IdP is a running identity provider.
type IdP struct {
	URL string // base; metadata at /metadata, SSO at /sso
	idp *saml.IdentityProvider

	mu      sync.Mutex
	session *saml.Session
}

// Start serves an identity provider until the test ends.
func Start(t *testing.T) *IdP {
	t.Helper()
	keyPEM, certPEM, err := sso.GenerateSPKey("test-idp")
	if err != nil {
		t.Fatal(err)
	}
	key, cert, err := sso.ParseSPKey(keyPEM, certPEM)
	if err != nil {
		t.Fatal(err)
	}
	i := &IdP{}
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	t.Cleanup(srv.Close)
	i.URL = srv.URL
	metadata, _ := url.Parse(srv.URL + "/metadata")
	ssoURL, _ := url.Parse(srv.URL + "/sso")
	i.idp = &saml.IdentityProvider{Key: key, Signer: key, Certificate: cert,
		MetadataURL: *metadata, SSOURL: *ssoURL,
		ServiceProviderProvider: spByMetadata{}, SessionProvider: i}
	srv.Config.Handler = i.idp.Handler()
	return i
}

// Key and Certificate sign the responses.
func (i *IdP) Key() *rsa.PrivateKey           { return i.idp.Key.(*rsa.PrivateKey) }
func (i *IdP) Certificate() *x509.Certificate { return i.idp.Certificate }

// MetadataURL is where the IdP's metadata is served.
func (i *IdP) MetadataURL() string { return i.URL + "/metadata" }

// MetadataXML is the IdP's metadata.
func (i *IdP) MetadataXML() string {
	b, _ := xml.Marshal(i.idp.Metadata())
	return string(b)
}

// SignInAs makes the next sign-ins s (nil: nobody signed in, refused).
func (i *IdP) SignInAs(s *saml.Session) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if s != nil {
		cp := *s
		if cp.ID == "" {
			cp.ID = fmt.Sprintf("session-%d", time.Now().UnixNano())
		}
		cp.CreateTime, cp.ExpireTime = time.Now(), time.Now().Add(time.Hour)
		s = &cp
	}
	i.session = s
}

// GetSession is the IdP's session: whoever SignInAs chose.
func (i *IdP) GetSession(w http.ResponseWriter, _ *http.Request, _ *saml.IdpAuthnRequest) *saml.Session {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.session == nil {
		http.Error(w, "nobody signed in", http.StatusForbidden)
	}
	return i.session
}

// IdPInitiated returns the auto-posting form of an IdP-initiated sign-in
// to the SP spEntityID.
func (i *IdP) IdPInitiated(spEntityID, relayState string) []byte {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, i.URL+"/start", nil)
	i.idp.ServeIDPInitiated(rec, req, spEntityID, relayState)
	return rec.Body.Bytes()
}

// spByMetadata fetches an SP's metadata from its entity id (its
// metadata URL).
type spByMetadata struct{}

func (spByMetadata) GetServiceProvider(_ *http.Request, id string) (*saml.EntityDescriptor, error) {
	resp, err := http.Get(id)
	if err != nil {
		return nil, os.ErrNotExist
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, os.ErrNotExist
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var ed saml.EntityDescriptor
	if err := xml.Unmarshal(b, &ed); err != nil {
		return nil, err
	}
	return &ed, nil
}
