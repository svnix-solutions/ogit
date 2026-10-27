package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/sso"
)

// SAML rules:
//   - a SAML identity provider is a provider of kind saml: its metadata
//     comes from a URL (refreshed daily; the last good one is kept if a
//     refresh fails) or is pasted; oGit's SP metadata, with its signing and
//     encryption certificate, is at /api/v1/auth/sso/<slug>/metadata (the
//     key pair is made with the provider, the key sealed);
//   - sign-ins start here (SP-initiated): the AuthnRequest is signed and
//     its id kept in the flow; the response must answer it, be signed and
//     be for this SP (audience, destination, times); encrypted assertions
//     are decrypted;
//   - the identity provider posts back cross-site, so the browser's flow
//     cookie isn't sent: the assertion consumer only verifies the answer
//     and keeps the claims in the flow, then sends the browser to finish,
//     which checks the cookie (like the other providers' callbacks);
//   - sign-ins started at the identity provider (its dashboard) are
//     refused unless allow_idp_initiated: they can't be tied to a browser,
//     so each assertion is accepted once (replays refused) and signs in at
//     once;
//   - the claims: NameID (or subject_attribute), email, user name, name
//     and groups from attributes (usual names, or the configured ones); the
//     identity provider's email counts as verified. The provider's MFA is
//     trusted like OIDC's.

var ErrSAMLReplay = errors.New("this sign-in was already used")

const samlMetadataTTL = 24 * time.Hour

type samlMetadata struct {
	version time.Time // the provider's UpdatedAt
	fetched time.Time
	idp     *saml.EntityDescriptor
}

var samlCache sync.Map // slug -> samlMetadata

func samlSettings(p *domain.SSOProvider) domain.SAMLSettings {
	var s domain.SAMLSettings
	if p.SAMLConfig != "" {
		_ = json.Unmarshal([]byte(p.SAMLConfig), &s)
	}
	return s
}

// SAMLSettingsOf are a SAML provider's settings (nil for other kinds).
func SAMLSettingsOf(p *domain.SSOProvider) *domain.SAMLSettings {
	if p.Kind != domain.SSOKindSAML {
		return nil
	}
	s := samlSettings(p)
	return &s
}

func (c *SSOApp) ssoURL(slug, what string) string {
	return strings.TrimSuffix(c.Config.Server.PublicURL, "/") + "/api/v1/auth/sso/" + url.PathEscape(slug) + "/" + what
}

// idpMetadata returns a provider's IdP metadata: pasted, or fetched (once
// a day).
func (c *SSOApp) idpMetadata(ctx context.Context, p *domain.SSOProvider, s domain.SAMLSettings) (*saml.EntityDescriptor, error) {
	if strings.TrimSpace(s.MetadataXML) != "" {
		return sso.ParseIDPMetadata([]byte(s.MetadataXML))
	}
	if v, ok := samlCache.Load(p.Slug); ok {
		m := v.(samlMetadata)
		if m.version.Equal(p.UpdatedAt) && time.Since(m.fetched) < samlMetadataTTL {
			return m.idp, nil
		}
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	idp, err := sso.FetchIDPMetadata(ctx, hc, p.Issuer)
	if err != nil {
		if v, ok := samlCache.Load(p.Slug); ok && v.(samlMetadata).version.Equal(p.UpdatedAt) {
			c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", p.Slug).Msg("SAML metadata refresh failed; keeping the last one")
			return v.(samlMetadata).idp, nil
		}
		return nil, err
	}
	samlCache.Store(p.Slug, samlMetadata{version: p.UpdatedAt, fetched: time.Now(), idp: idp})
	return idp, nil
}

// samlClient is the SP for a SAML provider.
func (c *SSOApp) samlClient(ctx context.Context, p *domain.SSOProvider) (sso.SAML, error) {
	s := samlSettings(p)
	keyPEM := p.ClientSecret
	if !p.FromConfig && keyPEM != "" {
		if c.Box == nil {
			return sso.SAML{}, ErrMirrorNeedsKey
		}
		plain, err := c.Box.Open(keyPEM)
		if err != nil {
			return sso.SAML{}, err
		}
		keyPEM = plain
	}
	key, cert, err := sso.ParseSPKey(keyPEM, s.Certificate)
	if err != nil {
		return sso.SAML{}, err
	}
	idp, err := c.idpMetadata(ctx, p, s)
	if err != nil {
		return sso.SAML{}, err
	}
	return sso.SAML{EntityID: c.ssoURL(p.Slug, "metadata"), ACS: c.ssoURL(p.Slug, "acs"), Key: key, Certificate: cert, IDP: idp,
		Username: s.UsernameAttribute, Email: s.EmailAttribute, Name: s.NameAttribute, Subject: s.SubjectAttribute, Groups: p.GroupsClaim}, nil
}

// SPMetadata is this server's SP metadata for a SAML provider.
func (c *SSOApp) SPMetadata(ctx context.Context, slug string) ([]byte, error) {
	p, err := c.provider(slug)
	if err != nil {
		return nil, err
	}
	if p.Kind != domain.SSOKindSAML || c.Config.Server.PublicURL == "" {
		return nil, ErrSSOProviderNotFound
	}
	s, err := c.samlClient(ctx, p)
	if err != nil {
		// The SP's metadata doesn't need the IdP's: answer without it.
		st := samlSettings(p)
		keyPEM := p.ClientSecret
		if !p.FromConfig && c.Box != nil && keyPEM != "" {
			keyPEM, _ = c.Box.Open(keyPEM)
		}
		key, cert, kerr := sso.ParseSPKey(keyPEM, st.Certificate)
		if kerr != nil {
			return nil, err
		}
		s = sso.SAML{EntityID: c.ssoURL(p.Slug, "metadata"), ACS: c.ssoURL(p.Slug, "acs"), Key: key, Certificate: cert,
			IDP: &saml.EntityDescriptor{}}
	}
	return s.Metadata()
}

// ACS takes the identity provider's answer. For a sign-in started here
// (relayState) it verifies the answer, keeps the claims in the flow and
// returns where to send the browser next (finish); for an IdP-initiated
// one (allowed by the provider) it signs in at once (out).
func (c *SSOApp) ACS(ctx context.Context, slug, samlResponse, relayState, userAgent, ip string) (next string, out *SSOOutcome, err error) {
	p, err := c.provider(slug)
	if err != nil {
		return "", nil, err
	}
	if !p.Enabled || p.Kind != domain.SSOKindSAML {
		return "", nil, ErrSSOProviderNotFound
	}
	s, err := c.samlClient(ctx, p)
	if err != nil {
		return "", nil, err
	}
	if relayState != "" {
		if flow, err := c.Pers.Flow(hashToken(relayState), c.now()); err == nil && flow.Provider == slug && flow.Result == "" {
			a, err := s.Parse(samlResponse, []string{flow.Nonce})
			if err != nil {
				c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", slug).Msg("SAML response refused")
				return "", nil, ErrSSONotAllowed
			}
			result, _ := json.Marshal(a.Claims)
			if err := c.Pers.SetFlowResult(flow.Id, string(result)); err != nil {
				return "", nil, err
			}
			return c.ssoURLPath(slug, "finish") + "?state=" + url.QueryEscape(relayState), nil, nil
		}
	}
	settings := samlSettings(p)
	if !settings.AllowIdPInitiated {
		return "", nil, ErrSSOFlow
	}
	s.AllowIdPInitiated = true
	a, err := s.Parse(samlResponse, nil)
	if err != nil {
		c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", slug).Msg("SAML response refused")
		return "", nil, ErrSSONotAllowed
	}
	if fresh, err := c.Pers.UseAssertion(hashToken(slug+"\x00"+a.ID), a.Expires); err != nil {
		return "", nil, err
	} else if !fresh {
		return "", nil, ErrSAMLReplay
	}
	if !domainAllowed(p, &a.Claims) {
		return "", nil, ErrSSONotAllowed
	}
	identity, err := c.Pers.Identity(slug, a.Claims.Subject)
	if err != nil && !isNotFound(err) {
		return "", nil, err
	}
	out, err = c.completeLogin(&SSOOutcome{Redirect: "/"}, p, identity, &a.Claims, userAgent, ip)
	return "", out, err
}

func (c *SSOApp) ssoURLPath(slug, what string) string {
	return "/api/v1/auth/sso/" + url.PathEscape(slug) + "/" + what
}

// Finish completes a SAML sign-in in the browser that started it.
func (c *SSOApp) Finish(slug, state, cookie, userAgent, ip string) (*SSOOutcome, error) {
	flow, err := c.Pers.TakeFlow(hashToken(state), c.now())
	if err != nil || flow.Provider != slug || cookie == "" || flow.BrowserKey != hashToken(cookie) || flow.Result == "" {
		return nil, ErrSSOFlow
	}
	p, err := c.provider(slug)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrSSOProviderNotFound
	}
	var claims sso.Claims
	if err := json.Unmarshal([]byte(flow.Result), &claims); err != nil {
		return nil, ErrSSOFlow
	}
	return c.afterClaims(&SSOOutcome{Redirect: flow.Redirect}, flow, p, &claims, userAgent, ip)
}

// checkSAML validates a SAML provider's settings, makes its SP key pair
// when it has none, and stores them.
func (c *SSOApp) checkSAML(p *domain.SSOProvider, in *domain.SAMLSettings) error {
	current := samlSettings(p)
	if in != nil {
		cert := current.Certificate
		current = *in
		current.Certificate = cert // made here, not given
	}
	if xmlData := strings.TrimSpace(current.MetadataXML); xmlData != "" {
		if _, err := sso.ParseIDPMetadata([]byte(xmlData)); err != nil {
			return errors.Join(ErrInvalidSSOProvider, err)
		}
	} else if u, err := url.Parse(p.Issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.Join(ErrInvalidSSOProvider, errors.New("set the identity provider's metadata URL or paste its metadata"))
	}
	if p.ClientSecret == "" || current.Certificate == "" {
		if p.FromConfig {
			return errors.Join(ErrInvalidSSOProvider, errors.New("SAML providers in the configuration need client_secret (the SP's private key, PEM) and saml.certificate"))
		}
		if c.Box == nil {
			return ErrMirrorNeedsKey
		}
		keyPEM, certPEM, err := sso.GenerateSPKey("ogit " + p.Slug)
		if err != nil {
			return err
		}
		if p.ClientSecret, err = c.Box.Seal(keyPEM); err != nil {
			return err
		}
		current.Certificate = certPEM
	}
	b, err := json.Marshal(current)
	if err != nil {
		return err
	}
	p.SAMLConfig = string(b)
	return nil
}
