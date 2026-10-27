package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/crewjam/saml/samlsp"
	dsig "github.com/russellhaering/goxmldsig"
)

var (
	ErrSAMLConfig   = errors.New("invalid SAML settings")
	ErrSAMLResponse = errors.New("the identity provider's answer was refused")
)

// SAML is a service provider (this server) for one identity provider.
type SAML struct {
	EntityID    string // this SP: its metadata URL
	ACS         string // assertion consumer service URL
	Key         *rsa.PrivateKey
	Certificate *x509.Certificate
	IDP         *saml.EntityDescriptor
	// AllowIdPInitiated accepts responses no request of ours asked for.
	AllowIdPInitiated bool
	// Attributes: the attribute names to read ("" for the usual ones);
	// Subject "" means the NameID.
	Username, Email, Name, Subject, Groups string
}

// GenerateSPKey makes the SP's key and a self-signed certificate (10
// years), both PEM.
func GenerateSPKey(commonName string) (keyPEM, certPEM string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return keyPEM, certPEM, nil
}

// ParseSPKey reads the SP's key and certificate.
func ParseSPKey(keyPEM, certPEM string) (*rsa.PrivateKey, *x509.Certificate, error) {
	kb, _ := pem.Decode([]byte(keyPEM))
	cb, _ := pem.Decode([]byte(certPEM))
	if kb == nil || cb == nil {
		return nil, nil, ErrSAMLConfig
	}
	key, err := x509.ParsePKCS1PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	return key, cert, err
}

// ParseIDPMetadata reads an identity provider's metadata (it must have a
// single sign-on service and a signing certificate).
func ParseIDPMetadata(xmlData []byte) (*saml.EntityDescriptor, error) {
	ed, err := samlsp.ParseMetadata(xmlData)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSAMLConfig, err)
	}
	if err := checkIDP(ed); err != nil {
		return nil, err
	}
	return ed, nil
}

// FetchIDPMetadata downloads an identity provider's metadata.
func FetchIDPMetadata(ctx context.Context, hc *http.Client, raw string) (*saml.EntityDescriptor, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: the metadata URL must be http(s)", ErrSAMLConfig)
	}
	ed, err := samlsp.FetchMetadata(ctx, hc, *u)
	if err != nil {
		return nil, fmt.Errorf("fetching the identity provider's metadata: %w", err)
	}
	if err := checkIDP(ed); err != nil {
		return nil, err
	}
	return ed, nil
}

func checkIDP(ed *saml.EntityDescriptor) error {
	if len(ed.IDPSSODescriptors) == 0 || len(ed.IDPSSODescriptors[0].SingleSignOnServices) == 0 {
		return fmt.Errorf("%w: the metadata has no single sign-on service", ErrSAMLConfig)
	}
	signing := false
	for _, kd := range ed.IDPSSODescriptors[0].KeyDescriptors {
		if (kd.Use == "" || kd.Use == "signing") && len(kd.KeyInfo.X509Data.X509Certificates) > 0 {
			signing = true
		}
	}
	if !signing {
		return fmt.Errorf("%w: the metadata has no signing certificate", ErrSAMLConfig)
	}
	return nil
}

func (s SAML) sp() *saml.ServiceProvider {
	entity, _ := url.Parse(s.EntityID)
	acs, _ := url.Parse(s.ACS)
	return &saml.ServiceProvider{EntityID: s.EntityID, Key: s.Key, Certificate: s.Certificate, MetadataURL: *entity, AcsURL: *acs,
		IDPMetadata: s.IDP, AllowIDPInitiated: s.AllowIdPInitiated, SignatureMethod: dsig.RSASHA256SignatureMethod,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat}
}

// Metadata is this SP's metadata, for the identity provider.
func (s SAML) Metadata() ([]byte, error) {
	return xml.MarshalIndent(s.sp().Metadata(), "", "  ")
}

// AuthnRequest is a signed request to the identity provider: a URL to
// redirect to, or (for IdPs without the redirect binding) an HTML form
// that posts itself.
type AuthnRequest struct {
	ID       string
	Redirect string
	PostForm []byte
}

// Request makes an authentication request carrying relayState.
func (s SAML) Request(relayState string) (*AuthnRequest, error) {
	sp := s.sp()
	if loc := sp.GetSSOBindingLocation(saml.HTTPRedirectBinding); loc != "" {
		req, err := sp.MakeAuthenticationRequest(loc, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
		if err != nil {
			return nil, err
		}
		u, err := req.Redirect(relayState, sp)
		if err != nil {
			return nil, err
		}
		return &AuthnRequest{ID: req.ID, Redirect: u.String()}, nil
	}
	loc := sp.GetSSOBindingLocation(saml.HTTPPostBinding)
	if loc == "" {
		return nil, fmt.Errorf("%w: the identity provider has no redirect or POST binding", ErrSAMLConfig)
	}
	req, err := sp.MakeAuthenticationRequest(loc, saml.HTTPPostBinding, saml.HTTPPostBinding)
	if err != nil {
		return nil, err
	}
	return &AuthnRequest{ID: req.ID, PostForm: req.Post(relayState)}, nil
}

// Assertion is a verified sign-in.
type Assertion struct {
	Claims  Claims
	ID      string    // the assertion's (replays)
	Expires time.Time // NotOnOrAfter
}

// Parse verifies a SAMLResponse (base64, from the POST binding): the
// signature, audience, destination, times and, unless requestIDs is empty
// and IdP-initiated sign-ins are allowed, that it answers one of
// requestIDs.
func (s SAML) Parse(samlResponse string, requestIDs []string) (*Assertion, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(samlResponse))
	if err != nil {
		return nil, fmt.Errorf("%w: not base64", ErrSAMLResponse)
	}
	acs, _ := url.Parse(s.ACS)
	a, err := s.sp().ParseXMLResponse(raw, requestIDs, *acs)
	if err != nil {
		var invalid *saml.InvalidResponseError
		if errors.As(err, &invalid) && invalid.PrivateErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrSAMLResponse, invalid.PrivateErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrSAMLResponse, err)
	}
	out := &Assertion{ID: a.ID, Expires: time.Now().Add(time.Hour)}
	if a.Conditions != nil && !a.Conditions.NotOnOrAfter.IsZero() {
		out.Expires = a.Conditions.NotOnOrAfter
	}
	attr := func(name string, defaults ...string) []string {
		names := defaults
		if name != "" {
			names = []string{name}
		}
		for _, st := range a.AttributeStatements {
			for _, at := range st.Attributes {
				for _, n := range names {
					if strings.EqualFold(at.Name, n) || strings.EqualFold(at.FriendlyName, n) {
						var vals []string
						for _, v := range at.Values {
							if v.Value != "" {
								vals = append(vals, v.Value)
							}
						}
						if len(vals) > 0 {
							return vals
						}
					}
				}
			}
		}
		return nil
	}
	first := func(vals []string) string {
		if len(vals) == 0 {
			return ""
		}
		return vals[0]
	}
	c := &out.Claims
	if a.Subject != nil && a.Subject.NameID != nil {
		c.Subject = a.Subject.NameID.Value
	}
	if s.Subject != "" {
		c.Subject = first(attr(s.Subject))
	}
	c.Email = first(attr(s.Email, "email", "mail", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "urn:oid:0.9.2342.19200300.100.1.3"))
	c.EmailVerified = c.Email != "" // the identity provider's own
	c.Username = first(attr(s.Username, "username", "uid", "preferred_username", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name", "urn:oid:0.9.2342.19200300.100.1.1"))
	c.Name = first(attr(s.Name, "name", "displayName", "cn", "urn:oid:2.16.840.1.113730.3.1.241"))
	c.Groups = attr(s.Groups, "groups", "memberOf", "http://schemas.microsoft.com/ws/2008/06/identity/claims/groups", "Role")
	if c.Subject == "" {
		return nil, ErrNoSubject
	}
	return out, nil
}
