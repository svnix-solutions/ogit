package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/labbs/ogit/infrastructure/sso"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

// Single sign-on rules:
//   - providers are OpenID Connect issuers or GitHub (OAuth2, optionally
//     limited to organizations); site admins manage them (with sudo; client
//     secrets sealed with security.encryption_key), and providers declared
//     in the configuration file (sso.providers) are shown read-only; they
//     need http.public_url (the callback URL);
//   - a sign-in is a flow used once, within 10 minutes, finished by the
//     browser that started it (a cookie), with PKCE and, for OIDC, a nonce;
//     redirects after it stay on this site;
//   - an identity (provider, subject) belongs to one user. The first
//     sign-in of an unknown identity: if a user has the same email and the
//     provider verified it, that user confirms with their password (and
//     2FA code) to link it; otherwise a new account is made (auto_create,
//     a verified email, allowed email domains), with a user name from the
//     provider made unique and no password;
//   - the provider's MFA is trusted: no oGit 2FA prompt and the session
//     counts as two-factor, unless the provider has require_two_factor
//     (users with oGit 2FA are then asked for it). Users without a password
//     whose identities are all at trusted providers count as having 2FA
//     everywhere (tokens, groups, admin) since every sign-in goes through
//     the provider;
//   - signed-in users link more providers and unlink them (sudo; never
//     their last way to sign in), and confirm sudo through a provider; users
//     without a password set one with sudo;
//   - disabled accounts can't sign in; logins are audit-logged.

var (
	ErrSSOProviderNotFound = errors.New("sign-in provider not found")
	ErrInvalidSSOProvider  = errors.New("invalid provider: check the slug (a-z, 0-9, -), name, kind (oidc or github), issuer URL and client id")
	ErrSSOProviderExists   = errors.New("a provider with this slug already exists")
	ErrSSOProviderReadOnly = errors.New("this provider comes from the configuration file; change it there")
	ErrSSONeedsPublicURL   = errors.New("sign-in providers need http.public_url (their callback URL)")
	ErrSSOFlow             = errors.New("this sign-in expired or was started in another browser; try again")
	ErrSSONotAllowed       = errors.New("this account isn't allowed to sign in here")
	ErrSSONoAccount        = errors.New("there is no account for this sign-in; ask an administrator")
	ErrSSOVerifiedEmail    = errors.New("the provider didn't share a verified email address")
	ErrSSOEmailTaken       = errors.New("an account already uses this email address: sign in to it and link this provider in your settings")
	ErrIdentityTaken       = errors.New("this account at the provider is linked to another user")
	ErrIdentityNotFound    = errors.New("linked account not found")
	ErrLastSignInMethod    = errors.New("this is your only way to sign in: set a password or link another provider first")
	ErrLinkExpired         = errors.New("this link request expired; sign in again")
	ErrLinkNeedsCode       = errors.New("enter your two-factor code too")
)

const (
	ssoFlowTTL   = 10 * time.Minute
	ssoLinkTTL   = 15 * time.Minute
	SSOCookie    = "ogit_sso"
	ssoUserAgent = "oGit"
)

type SSOApp struct {
	Config    config.Config
	Logger    zerolog.Logger
	Pers      domain.SSOPers
	Auth      *AuthApp
	Users     domain.UserPers
	Repos     *RepositoryApp
	TwoFactor *TwoFactorApp
	Admin     *AdminApp
	Groups    domain.GroupPers       // group sync
	Settings  domain.SiteSettingPers // sso_only from the admin page
	Box       *secretbox.Box
	// FromConfig: providers declared in the configuration file.
	FromConfig []domain.SSOProvider
	HTTPClient *http.Client
	Now        func() time.Time

	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	version time.Time
	client  sso.Client
}

func (c *SSOApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

func hashToken(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// providers lists every provider: the configuration's, then the stored
// ones (a slug taken by the configuration hides a stored one).
func (c *SSOApp) providers() ([]domain.SSOProvider, error) {
	stored, err := c.Pers.Providers()
	if err != nil {
		return nil, err
	}
	out := append([]domain.SSOProvider{}, c.FromConfig...)
	for _, p := range stored {
		if c.configured(p.Slug) == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *SSOApp) configured(slug string) *domain.SSOProvider {
	for i := range c.FromConfig {
		if c.FromConfig[i].Slug == slug {
			return &c.FromConfig[i]
		}
	}
	return nil
}

func (c *SSOApp) provider(slug string) (*domain.SSOProvider, error) {
	if p := c.configured(slug); p != nil {
		return p, nil
	}
	p, err := c.Pers.Provider(slug)
	if isNotFound(err) {
		return nil, ErrSSOProviderNotFound
	}
	return p, err
}

// Enabled lists the providers on the sign-in page.
func (c *SSOApp) Enabled() ([]domain.SSOProvider, error) {
	if c == nil || c.Config.Server.PublicURL == "" {
		return nil, nil
	}
	all, err := c.providers()
	var out []domain.SSOProvider
	for _, p := range all {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out, err
}

func (c *SSOApp) callbackURL(slug string) string {
	return strings.TrimSuffix(c.Config.Server.PublicURL, "/") + "/api/v1/auth/sso/" + url.PathEscape(slug) + "/callback"
}

// client returns a provider's client (cached until the provider changes).
func (c *SSOApp) client(ctx context.Context, p *domain.SSOProvider) (sso.Client, error) {
	c.mu.Lock()
	cached, ok := c.clients[p.Slug]
	c.mu.Unlock()
	if ok && cached.version.Equal(p.UpdatedAt) {
		return cached.client, nil
	}
	secret := p.ClientSecret
	if !p.FromConfig && secret != "" {
		if c.Box == nil {
			return nil, ErrMirrorNeedsKey
		}
		plain, err := c.Box.Open(secret)
		if err != nil {
			return nil, err
		}
		secret = plain
	}
	hc := c.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	client, err := sso.New(ctx, sso.Endpoint{Kind: p.Kind, Issuer: p.Issuer, ClientId: p.ClientId, ClientSecret: secret,
		RedirectURL: c.callbackURL(p.Slug), Scopes: strings.Fields(p.Scopes), AllowedOrgs: splitList(p.AllowedOrgs), GroupsClaim: p.GroupsClaim}, hc)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.clients == nil {
		c.clients = map[string]cachedClient{}
	}
	c.clients[p.Slug] = cachedClient{version: p.UpdatedAt, client: client}
	c.mu.Unlock()
	return client, nil
}

func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// SafeRedirect keeps a redirect on this site ("/" otherwise).
func SafeRedirect(r string) string {
	if !strings.HasPrefix(r, "/") || strings.HasPrefix(r, "//") || strings.HasPrefix(r, "/\\") || strings.ContainsAny(r, "\r\n") {
		return "/"
	}
	return r
}

// SSOStart is where to send the browser, and the cookie binding the flow
// to it.
type SSOStart struct {
	URL    string
	Cookie string
}

// Start begins a sign-in (purpose login), a link or a sudo confirmation
// (auth: the signed-in session).
func (c *SSOApp) Start(ctx context.Context, slug, purpose, redirect string, auth *fiberoapi.AuthContext) (*SSOStart, error) {
	if c.Config.Server.PublicURL == "" {
		return nil, ErrSSONeedsPublicURL
	}
	p, err := c.provider(slug)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrSSOProviderNotFound
	}
	flow := &domain.SSOFlow{Provider: p.Slug, Purpose: purpose, Nonce: randomToken(""), Verifier: sso.GenerateVerifier(),
		Redirect: SafeRedirect(redirect), ExpiresAt: c.now().Add(ssoFlowTTL), CreatedAt: c.now()}
	switch purpose {
	case domain.SSOPurposeLogin:
	case domain.SSOPurposeLink, domain.SSOPurposeSudo:
		if auth == nil || !domain.IsPerson(auth.UserID) {
			return nil, ErrForbidden
		}
		sid := sessionId(auth)
		if sid == "" {
			return nil, ErrSudoRequired
		}
		if purpose == domain.SSOPurposeLink {
			if err := c.TwoFactor.RequireSudo(auth); err != nil {
				return nil, err
			}
		}
		flow.UserId, flow.SessionId = &auth.UserID, &sid
	default:
		return nil, ErrSSOFlow
	}
	client, err := c.client(ctx, p)
	if err != nil {
		c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", p.Slug).Msg("provider unreachable")
		return nil, fmt.Errorf("the sign-in with %s failed: the provider can't be reached; try again later", p.Name)
	}
	state, cookie := randomToken(""), randomToken("")
	flow.Id, flow.BrowserKey = hashToken(state), hashToken(cookie)
	if err := c.Pers.CreateFlow(flow); err != nil {
		return nil, err
	}
	return &SSOStart{URL: client.AuthURL(state, flow.Nonce, flow.Verifier), Cookie: cookie}, nil
}

// SSOOutcome is what a callback did: a session (Login), a 2FA step
// (Challenge), a link to confirm (LinkToken), or nothing more (link and
// sudo flows); Redirect is where to go next.
type SSOOutcome struct {
	Login     *dtos.LoginResponse
	Challenge string
	Methods   []string
	LinkToken string
	Redirect  string
}

// Callback finishes a flow. providerError is the provider's error
// parameter, if any.
func (c *SSOApp) Callback(ctx context.Context, slug, state, code, providerError, cookie, userAgent, ip string) (*SSOOutcome, error) {
	flow, err := c.Pers.TakeFlow(hashToken(state), c.now())
	if err != nil || flow.Provider != slug || cookie == "" || flow.BrowserKey != hashToken(cookie) {
		return nil, ErrSSOFlow
	}
	out := &SSOOutcome{Redirect: flow.Redirect}
	p, err := c.provider(slug)
	if err != nil {
		return nil, err
	}
	if !p.Enabled {
		return nil, ErrSSOProviderNotFound
	}
	if providerError != "" {
		return nil, fmt.Errorf("%w (%s)", ErrSSONotAllowed, providerError)
	}
	client, err := c.client(ctx, p)
	if err != nil {
		return nil, err
	}
	claims, err := client.Exchange(ctx, code, flow.Verifier, flow.Nonce)
	if errors.Is(err, sso.ErrNotAllowed) {
		return nil, ErrSSONotAllowed
	} else if err != nil {
		c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", slug).Msg("sign-in failed at the provider")
		return nil, fmt.Errorf("the sign-in with %s failed: %w", p.Name, err)
	}
	if !domainAllowed(p, claims) {
		return nil, ErrSSONotAllowed
	}
	identity, err := c.Pers.Identity(slug, claims.Subject)
	if err != nil && !isNotFound(err) {
		return nil, err
	}

	switch flow.Purpose {
	case domain.SSOPurposeSudo:
		if identity == nil || flow.UserId == nil || identity.UserId != *flow.UserId {
			return nil, ErrSSONotAllowed
		}
		until := c.now().Add(SudoWindow)
		return out, c.TwoFactor.Sessions.SetSudoUntil(*flow.SessionId, until)
	case domain.SSOPurposeLink:
		if identity != nil {
			if identity.UserId != *flow.UserId {
				return nil, ErrIdentityTaken
			}
			return out, nil
		}
		if err := c.Pers.CreateIdentity(&domain.UserIdentity{UserId: *flow.UserId, Provider: slug, Subject: claims.Subject, Email: claims.Email,
			Username: claims.Username, CreatedAt: c.now()}); err != nil {
			return nil, err
		}
		c.audit(*flow.UserId, "sso.link").Str("provider", slug).Msg("sign-in provider linked")
		return out, nil
	}

	var user domain.User
	if identity != nil {
		if user, err = c.Users.GetById(identity.UserId); err != nil {
			return nil, err
		}
	} else {
		if claims.Email != "" && claims.EmailVerified {
			if existing, err := c.Users.GetByEmail(claims.Email); err == nil && existing.Id != "" {
				// Theirs if they say so with their password.
				if existing.Password == "" {
					return nil, ErrSSOEmailTaken
				}
				token := randomToken("")
				groups, _ := json.Marshal(claims.Groups)
				err := c.Pers.CreatePendingLink(&domain.SSOPendingLink{Id: hashToken(token), UserId: existing.Id, Provider: slug, Subject: claims.Subject,
					Email: claims.Email, Username: claims.Username, Groups: string(groups), Redirect: flow.Redirect, ExpiresAt: c.now().Add(ssoLinkTTL), CreatedAt: c.now()})
				if err != nil {
					return nil, err
				}
				out.LinkToken = token
				return out, nil
			}
		}
		if !p.AutoCreate {
			return nil, ErrSSONoAccount
		}
		if claims.Email == "" || !claims.EmailVerified {
			return nil, ErrSSOVerifiedEmail
		}
		if user, err = c.createUser(p, claims); err != nil {
			return nil, err
		}
		identity = &domain.UserIdentity{UserId: user.Id, Provider: slug, Subject: claims.Subject, Email: claims.Email, Username: claims.Username, CreatedAt: c.now()}
		if err := c.Pers.CreateIdentity(identity); err != nil {
			return nil, err
		}
	}
	if !user.Active {
		return nil, ErrAccountDisabled
	}
	_ = c.Pers.TouchIdentity(identity.Id, c.now())
	c.syncGroups(p, &user, claims.Groups)
	return c.signIn(out, p, &user, userAgent, ip)
}

// signIn opens the session, or asks for oGit 2FA when the provider
// requires it.
func (c *SSOApp) signIn(out *SSOOutcome, p *domain.SSOProvider, user *domain.User, userAgent, ip string) (*SSOOutcome, error) {
	if p.RequireTwoFactor {
		on, err := c.TwoFactor.Enabled(user.Id)
		if err != nil {
			return nil, err
		}
		if on {
			challenge, err := c.TwoFactor.StartLogin(user, userAgent, ip)
			if err != nil {
				return nil, err
			}
			methods, err := c.TwoFactor.Methods(user.Id)
			if err != nil {
				return nil, err
			}
			out.Challenge, out.Methods = challenge, methods
			return out, nil
		}
	}
	var trusted *string
	if !p.RequireTwoFactor {
		trusted = &p.Slug
	}
	login, err := c.Auth.newSessionVia(user.Id, userAgent, ip, trusted)
	if err != nil {
		return nil, err
	}
	c.audit(user.Id, "sso.login").Str("provider", p.Slug).Msg("signed in through a provider")
	out.Login = login
	return out, nil
}

func domainAllowed(p *domain.SSOProvider, claims *sso.Claims) bool {
	domains := splitList(p.AllowedDomains)
	if len(domains) == 0 {
		return true
	}
	_, d, ok := strings.Cut(claims.Email, "@")
	if !ok || !claims.EmailVerified {
		return false
	}
	for _, want := range domains {
		if strings.EqualFold(d, strings.TrimPrefix(want, "@")) {
			return true
		}
	}
	return false
}

var nameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// createUser makes an account for a new identity: a user name from the
// provider (made a valid, unused namespace), no password, the provider's
// verified email.
func (c *SSOApp) createUser(p *domain.SSOProvider, claims *sso.Claims) (domain.User, error) {
	base := claims.Username
	if base == "" {
		base, _, _ = strings.Cut(claims.Email, "@")
	}
	base = strings.Trim(nameChars.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if len(base) > 30 {
		base = base[:30]
	}
	if base == "" {
		base = "user"
	}
	if checkNewNamespace(c.Repos.UserPers, c.Repos.GroupPers, base) == domain.ErrInvalidNamespaceName {
		base = "user-" + base // a reserved word
	}
	var name string
	for i := 0; ; i++ {
		name = base
		if i > 0 {
			name = base + "-" + strconv.Itoa(i+1)
		}
		if i >= 50 {
			name = base + "-" + randomToken("")[:6]
		}
		err := checkNewNamespace(c.Repos.UserPers, c.Repos.GroupPers, name)
		if err == nil {
			break
		}
		if i > 60 || err != ErrNamespaceTaken {
			return domain.User{}, fmt.Errorf("choosing a user name: %w", err)
		}
	}
	now := c.now()
	user := domain.User{Id: utils.UUIDv4(), Username: name, Email: claims.Email, Active: true, Role: domain.RoleUser,
		EmailVerifiedAt: &now, CreatedAt: now, UpdatedAt: now}
	if err := c.Users.Create(&user); err != nil {
		return domain.User{}, err
	}
	c.audit(user.Id, "sso.create").Str("provider", p.Slug).Str("username", name).Msg("account created through a provider")
	return user, nil
}

// LinkInfo describes a pending link (for the confirmation page).
type LinkInfo struct {
	Provider string // name
	Email    string
	Username string // the account's here
}

func (c *SSOApp) LinkInfo(token string) (*LinkInfo, error) {
	l, err := c.Pers.PendingLink(hashToken(token), c.now())
	if err != nil {
		return nil, ErrLinkExpired
	}
	p, err := c.provider(l.Provider)
	if err != nil {
		return nil, err
	}
	user, err := c.Users.GetById(l.UserId)
	if err != nil {
		return nil, ErrLinkExpired
	}
	return &LinkInfo{Provider: p.Name, Email: l.Email, Username: user.Username}, nil
}

// LinkUser names the account a pending link is for (rate limits).
func (c *SSOApp) LinkUser(token string) (*domain.User, error) {
	l, err := c.Pers.PendingLink(hashToken(token), c.now())
	if err != nil {
		return nil, ErrLinkExpired
	}
	user, err := c.Users.GetById(l.UserId)
	if err != nil {
		return nil, ErrLinkExpired
	}
	return &user, nil
}

// CompleteLink links a pending identity after the account's password (and
// 2FA code when it has 2FA), and signs in. It returns where to go next.
func (c *SSOApp) CompleteLink(token, password, code, userAgent, ip string) (*dtos.LoginResponse, string, error) {
	l, err := c.Pers.PendingLink(hashToken(token), c.now())
	if err != nil {
		return nil, "", ErrLinkExpired
	}
	user, err := c.Users.GetById(l.UserId)
	if err != nil {
		return nil, "", ErrLinkExpired
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
		return nil, "", ErrInvalidCredentials
	}
	if !user.Active {
		return nil, "", ErrAccountDisabled
	}
	on, err := c.TwoFactor.Enabled(user.Id)
	if err != nil {
		return nil, "", err
	}
	if on {
		if code == "" {
			return nil, "", ErrLinkNeedsCode
		}
		if err := c.TwoFactor.Verify(user.Id, code); err != nil {
			return nil, "", err
		}
	}
	if l, err = c.Pers.TakePendingLink(hashToken(token), c.now()); err != nil {
		return nil, "", ErrLinkExpired
	}
	p, err := c.provider(l.Provider)
	if err != nil {
		return nil, "", err
	}
	if existing, err := c.Pers.Identity(l.Provider, l.Subject); err == nil {
		if existing.UserId != user.Id {
			return nil, "", ErrIdentityTaken
		}
	} else if err := c.Pers.CreateIdentity(&domain.UserIdentity{UserId: user.Id, Provider: l.Provider, Subject: l.Subject, Email: l.Email,
		Username: l.Username, LastLoginAt: ptr(c.now()), CreatedAt: c.now()}); err != nil {
		return nil, "", err
	}
	c.audit(user.Id, "sso.link").Str("provider", l.Provider).Msg("sign-in provider linked by email")
	var groups []string
	_ = json.Unmarshal([]byte(l.Groups), &groups)
	c.syncGroups(p, &user, groups)
	var trusted *string
	if !p.RequireTwoFactor {
		trusted = &p.Slug
	}
	login, err := c.Auth.newSessionVia(user.Id, userAgent, ip, trusted)
	return login, l.Redirect, err
}

func ptr[T any](v T) *T { return &v }

// LinkedIdentity is an identity with its provider's name.
type LinkedIdentity struct {
	domain.UserIdentity
	ProviderName string
}

// Identities lists a user's linked providers.
func (c *SSOApp) Identities(userId string) ([]LinkedIdentity, error) {
	ids, err := c.Pers.Identities(userId)
	if err != nil {
		return nil, err
	}
	out := make([]LinkedIdentity, len(ids))
	for i, id := range ids {
		out[i] = LinkedIdentity{UserIdentity: id, ProviderName: id.Provider}
		if p, err := c.provider(id.Provider); err == nil {
			out[i].ProviderName = p.Name
		}
	}
	return out, nil
}

// Unlink removes a linked provider (sudo), never the last way to sign in.
func (c *SSOApp) Unlink(auth *fiberoapi.AuthContext, id string) error {
	if err := c.TwoFactor.RequireSudo(auth); err != nil {
		return err
	}
	user, err := c.Users.GetById(auth.UserID)
	if err != nil {
		return err
	}
	ids, err := c.Pers.Identities(user.Id)
	if err != nil {
		return err
	}
	found := false
	for _, i := range ids {
		found = found || i.Id == id
	}
	if !found {
		return ErrIdentityNotFound
	}
	if !c.passwordUsable(&user) && len(ids) == 1 {
		return ErrLastSignInMethod
	}
	if err := c.Pers.DeleteIdentity(user.Id, id); err != nil {
		return err
	}
	c.audit(user.Id, "sso.unlink").Str("identity", id).Msg("sign-in provider unlinked")
	return nil
}

// Trusted: the user signs in only through providers whose MFA is trusted
// (no password, and every identity at such a provider): they count as
// having two-factor authentication.
func (c *SSOApp) Trusted(userId string) (bool, error) {
	if c == nil {
		return false, nil
	}
	user, err := c.Users.GetById(userId)
	if err != nil || c.passwordUsable(&user) {
		return false, nil
	}
	ids, err := c.Pers.Identities(userId)
	if err != nil || len(ids) == 0 {
		return false, err
	}
	for _, i := range ids {
		p, err := c.provider(i.Provider)
		if err != nil || !p.Enabled || p.RequireTwoFactor {
			return false, nil
		}
	}
	return true, nil
}

// SetFirstPassword lets a user without a password (made through a
// provider) set one, with sudo.
func (c *SSOApp) SetFirstPassword(auth *fiberoapi.AuthContext, next string) error {
	if err := c.TwoFactor.RequireSudo(auth); err != nil {
		return err
	}
	user, err := c.Users.GetById(auth.UserID)
	if err != nil {
		return err
	}
	if user.Password != "" {
		return ErrInvalidCredentials
	}
	if c.PasswordBlocked(&user) {
		return ErrPasswordLoginDisabled
	}
	if err := checkPasswordPolicy(c.Config, next); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), PasswordCost)
	if err != nil {
		return err
	}
	if err := c.Users.SetPassword(user.Id, string(hash)); err != nil {
		return err
	}
	c.audit(user.Id, "password.set").Msg("first password set")
	return nil
}

func (c *SSOApp) audit(userId, action string) *zerolog.Event {
	return c.Logger.Info().Str("component", "audit").Str("user_id", userId).Str("action", action)
}

// ProviderInput describes a provider (nil: unchanged).
type ProviderInput struct {
	Slug, Name, Kind, Issuer, ClientId    *string
	ClientSecret                          *string // write-only
	Scopes, AllowedDomains, AllowedOrgs   *string
	AutoCreate, RequireTwoFactor, Enabled *bool
	GroupsClaim, AdminGroup               *string
	GroupMappings                         *[]domain.SSOGroupMapping
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func (c *SSOApp) apply(p *domain.SSOProvider, in ProviderInput) error {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&p.Slug, in.Slug)
	set(&p.Name, in.Name)
	set(&p.Kind, in.Kind)
	set(&p.Issuer, in.Issuer)
	set(&p.ClientId, in.ClientId)
	set(&p.Scopes, in.Scopes)
	set(&p.AllowedDomains, in.AllowedDomains)
	set(&p.AllowedOrgs, in.AllowedOrgs)
	set(&p.GroupsClaim, in.GroupsClaim)
	set(&p.AdminGroup, in.AdminGroup)
	if in.GroupMappings != nil {
		encoded, err := encodeMappings(*in.GroupMappings)
		if err != nil {
			return err
		}
		p.GroupMappings = encoded
	}
	for dst, v := range map[*bool]*bool{&p.AutoCreate: in.AutoCreate, &p.RequireTwoFactor: in.RequireTwoFactor, &p.Enabled: in.Enabled} {
		if v != nil {
			*dst = *v
		}
	}
	if p.Kind == domain.SSOKindGitHub && p.Issuer == "" {
		p.Issuer = "https://github.com"
	}
	u, err := url.Parse(p.Issuer)
	if !slugPattern.MatchString(p.Slug) || p.Name == "" || (p.Kind != domain.SSOKindOIDC && p.Kind != domain.SSOKindGitHub) ||
		err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || p.ClientId == "" {
		return ErrInvalidSSOProvider
	}
	if in.ClientSecret != nil {
		p.ClientSecret = ""
		if *in.ClientSecret != "" {
			if c.Box == nil {
				return ErrMirrorNeedsKey
			}
			if p.ClientSecret, err = c.Box.Seal(*in.ClientSecret); err != nil {
				return err
			}
		}
	}
	return nil
}

// adminCheck: site admins, with sudo.
func (c *SSOApp) adminCheck(auth *fiberoapi.AuthContext) error {
	if err := c.Admin.RequireAdmin(auth.UserID); err != nil {
		return err
	}
	return c.TwoFactor.RequireSudo(auth)
}

// AdminProviders lists every provider (admins).
func (c *SSOApp) AdminProviders(actorId string) ([]domain.SSOProvider, error) {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	return c.providers()
}

// CallbackURL is a provider's redirect URI, to register at the provider.
func (c *SSOApp) CallbackURL(slug string) string {
	if c.Config.Server.PublicURL == "" {
		return ""
	}
	return c.callbackURL(slug)
}

func (c *SSOApp) CreateProvider(auth *fiberoapi.AuthContext, in ProviderInput) (*domain.SSOProvider, error) {
	if err := c.adminCheck(auth); err != nil {
		return nil, err
	}
	if c.Config.Server.PublicURL == "" {
		return nil, ErrSSONeedsPublicURL
	}
	p := &domain.SSOProvider{AutoCreate: true, Enabled: true, GroupMappings: "[]", CreatedAt: c.now(), UpdatedAt: c.now()}
	if err := c.apply(p, in); err != nil {
		return nil, err
	}
	if c.configured(p.Slug) != nil {
		return nil, ErrSSOProviderExists
	}
	if _, err := c.Pers.Provider(p.Slug); err == nil {
		return nil, ErrSSOProviderExists
	}
	if p.ClientSecret == "" {
		return nil, ErrInvalidSSOProvider
	}
	if err := c.Pers.CreateProvider(p); err != nil {
		return nil, err
	}
	c.audit(auth.UserID, "sso.provider.create").Str("provider", p.Slug).Msg("sign-in provider added")
	return p, nil
}

func (c *SSOApp) UpdateProvider(auth *fiberoapi.AuthContext, slug string, in ProviderInput) (*domain.SSOProvider, error) {
	if err := c.adminCheck(auth); err != nil {
		return nil, err
	}
	if c.configured(slug) != nil {
		return nil, ErrSSOProviderReadOnly
	}
	p, err := c.provider(slug)
	if err != nil {
		return nil, err
	}
	in.Slug = nil // identities refer to it
	if err := c.apply(p, in); err != nil {
		return nil, err
	}
	p.UpdatedAt = c.now()
	err = c.Pers.UpdateProvider(p.Id, map[string]any{"name": p.Name, "kind": p.Kind, "issuer": p.Issuer, "client_id": p.ClientId,
		"client_secret": p.ClientSecret, "scopes": p.Scopes, "allowed_domains": p.AllowedDomains, "allowed_orgs": p.AllowedOrgs,
		"auto_create": p.AutoCreate, "require_two_factor": p.RequireTwoFactor, "enabled": p.Enabled, "groups_claim": p.GroupsClaim,
		"admin_group": p.AdminGroup, "group_mappings": p.GroupMappings, "updated_at": p.UpdatedAt})
	if err != nil {
		return nil, err
	}
	c.audit(auth.UserID, "sso.provider.update").Str("provider", p.Slug).Msg("sign-in provider changed")
	return p, nil
}

// DeleteProvider removes a provider; its identities stay (unusable) so a
// provider added again with the slug finds them.
func (c *SSOApp) DeleteProvider(auth *fiberoapi.AuthContext, slug string) error {
	if err := c.adminCheck(auth); err != nil {
		return err
	}
	if c.configured(slug) != nil {
		return ErrSSOProviderReadOnly
	}
	p, err := c.provider(slug)
	if err != nil {
		return err
	}
	c.audit(auth.UserID, "sso.provider.delete").Str("provider", slug).Msg("sign-in provider removed")
	return c.Pers.DeleteProvider(p.Id)
}

// LoadConfig takes the providers of the configuration file.
func (c *SSOApp) LoadConfig(cfgs []config.SSOProviderConfig) error {
	c.FromConfig = nil
	for _, pc := range cfgs {
		p := domain.SSOProvider{Id: "config:" + pc.Slug, FromConfig: true, AutoCreate: true, Enabled: true, RequireTwoFactor: pc.RequireTwoFactor, ClientSecret: pc.ClientSecret}
		if pc.AutoCreate != nil {
			p.AutoCreate = *pc.AutoCreate
		}
		if pc.Enabled != nil {
			p.Enabled = *pc.Enabled
		}
		ms := make([]domain.SSOGroupMapping, len(pc.GroupMappings))
		for i, m := range pc.GroupMappings {
			ms[i] = domain.SSOGroupMapping{Group: m.Group, Target: m.Target, Role: domain.GroupRole(m.Role)}
		}
		in := ProviderInput{Slug: &pc.Slug, Name: &pc.Name, Kind: &pc.Kind, Issuer: &pc.Issuer, ClientId: &pc.ClientId, Scopes: &pc.Scopes,
			AllowedDomains: &pc.AllowedDomains, AllowedOrgs: &pc.AllowedOrgs, GroupsClaim: &pc.GroupsClaim, AdminGroup: &pc.AdminGroup, GroupMappings: &ms}
		if err := c.apply(&p, in); err != nil {
			return fmt.Errorf("sso.providers %q: %w", pc.Slug, err)
		}
		if c.configured(p.Slug) != nil {
			return fmt.Errorf("sso.providers %q: %w", pc.Slug, ErrSSOProviderExists)
		}
		c.FromConfig = append(c.FromConfig, p)
	}
	return nil
}

// Sweep deletes expired flows and pending links.
func (c *SSOApp) Sweep() error { return c.Pers.DeleteExpired(c.now()) }
