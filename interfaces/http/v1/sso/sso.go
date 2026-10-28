// Package sso serves single sign-on: the browser's trips to providers and
// back (plain routes, redirects), the sign-in page's providers, linking
// accounts by email, users' linked accounts, and the admins' providers.
package sso

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config   config.Config
	Logger   zerolog.Logger
	SSO      *application.SSOApp
	Sessions *application.SessionApp
	Limits   *ratelimit.Guard
}

// Setup registers the routes: auth and user and admin are the API's
// groups (/api/v1/auth, /user, /admin).
func Setup(c Controller, app *fiber.App, auth, user, admin *fiberoapi.OApiGroup) {
	app.Get("/api/v1/auth/sso/:provider/start", c.Start)
	app.Get("/api/v1/auth/sso/:provider/callback", c.Callback)
	// SAML: the assertion consumer (the IdP posts there), the browser's
	// next step, and this SP's metadata.
	app.Post("/api/v1/auth/sso/:provider/acs", c.ACS)
	app.Get("/api/v1/auth/sso/:provider/finish", c.Finish)
	app.Get("/api/v1/auth/sso/:provider/metadata", c.Metadata)

	tags := []string{"Single sign-on"}
	fiberoapi.Get(auth, "/providers", c.Providers, fiberoapi.OpenAPIOptions{
		Summary: "Sign-in providers", OperationID: "auth.providers", Tags: tags, Security: "disabled",
		Description: "The providers on the sign-in page: send the browser to /api/v1/auth/sso/<slug>/start?redirect=<path>.",
	})
	fiberoapi.Get(auth, "/sso/link", c.LinkInfo, fiberoapi.OpenAPIOptions{
		Summary: "A pending link", OperationID: "auth.sso.link.get", Tags: tags, Security: "disabled",
		Description: "?token= from the sign-in: the provider and the account (same email) it would be linked to.",
	})
	fiberoapi.Post(auth, "/sso/link", c.CompleteLink, fiberoapi.OpenAPIOptions{
		Summary: "Link a provider to an existing account", OperationID: "auth.sso.link", Tags: tags, Security: "disabled",
		Description: "The account's password (and 2FA code when it has 2FA) confirm the link; then signed in (cookies). Wrong passwords count as failed logins.",
	})
	fiberoapi.Get(user, "/identities", c.Identities, fiberoapi.OpenAPIOptions{
		Summary: "Linked sign-in providers", OperationID: "user.identities.list", Tags: tags,
		Description: "Link another with /api/v1/auth/sso/<slug>/start?purpose=link (sudo).",
	})
	fiberoapi.Delete(user, "/identities/:id", c.Unlink, fiberoapi.OpenAPIOptions{
		Summary: "Unlink a sign-in provider", OperationID: "user.identities.delete", Tags: tags,
		Description: "Sudo. Never the last way to sign in.",
	})
	fiberoapi.Get(admin, "/sso/providers", c.AdminProviders, fiberoapi.OpenAPIOptions{
		Summary: "Sign-in providers", OperationID: "admin.sso.providers.list", Tags: tags,
		Description: "Site admins: every provider (from_config ones are read-only), with the callback URL to register at the provider.",
	})
	fiberoapi.Post(admin, "/sso/providers", c.CreateProvider, fiberoapi.OpenAPIOptions{
		Summary: "Add a sign-in provider", OperationID: "admin.sso.providers.create", Tags: tags,
		Description: "Site admins, sudo. kind: oidc (issuer: the OpenID issuer URL) or github (issuer: https://github.com or an Enterprise server; allowed_orgs). Needs http.public_url and security.encryption_key.",
	})
	fiberoapi.Patch(admin, "/sso/providers/:slug", c.UpdateProvider, fiberoapi.OpenAPIOptions{
		Summary: "Change a sign-in provider", OperationID: "admin.sso.providers.update", Tags: tags,
	})
	fiberoapi.Delete(admin, "/sso/providers/:slug", c.DeleteProvider, fiberoapi.OpenAPIOptions{
		Summary: "Remove a sign-in provider", OperationID: "admin.sso.providers.delete", Tags: tags,
		Description: "Linked accounts stay: a provider added again with the slug finds them.",
	})
}

func (c Controller) secure(ctx *fiber.Ctx) bool {
	return websession.Secure(ctx, c.Config.Server.PublicURL)
}

// fail sends the browser back with the error to show.
func (c Controller) fail(ctx *fiber.Ctx, to string, err error) error {
	msg := err.Error()
	var known bool
	for _, e := range []error{application.ErrSSOFlow, application.ErrSSONotAllowed, application.ErrSSONoAccount, application.ErrSSOVerifiedEmail,
		application.ErrSSOEmailTaken, application.ErrIdentityTaken, application.ErrAccountDisabled, application.ErrSSOProviderNotFound,
		application.ErrSSONeedsPublicURL, application.ErrSudoRequired, application.ErrSAMLReplay} {
		known = known || errors.Is(err, e)
	}
	if !known && !strings.HasPrefix(msg, "the sign-in with ") {
		c.Logger.Error().Err(err).Str("component", "http.sso").Msg("sign-in failed")
		msg = "the sign-in failed; try again"
	}
	u, _ := url.Parse(application.SafeRedirect(to))
	q := u.Query()
	q.Set("sso_error", msg)
	u.RawQuery = q.Encode()
	return ctx.Redirect(u.String(), fiber.StatusFound)
}

// Start sends the browser to the provider.
func (c Controller) Start(ctx *fiber.Ctx) error {
	purpose := ctx.Query("purpose", domain.SSOPurposeLogin)
	redirect := application.SafeRedirect(ctx.Query("redirect", "/"))
	back := "/login"
	var auth *fiberoapi.AuthContext
	if purpose != domain.SSOPurposeLogin {
		back = redirect
		if token, ok := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer "); ok {
			auth, _ = c.Sessions.ValidateToken(token)
		}
		if auth == nil {
			return c.fail(ctx, "/login", application.ErrSSOFlow)
		}
	}
	start, err := c.SSO.Start(ctx.UserContext(), ctx.Params("provider"), purpose, redirect, auth)
	if err != nil {
		return c.fail(ctx, back, err)
	}
	ctx.Cookie(&fiber.Cookie{Name: application.SSOCookie, Value: start.Cookie, Path: "/api/v1/auth/sso/", MaxAge: 600,
		HTTPOnly: true, Secure: c.secure(ctx), SameSite: fiber.CookieSameSiteLaxMode})
	if len(start.Form) > 0 { // a SAML request posted to the IdP
		ctx.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return ctx.Send(start.Form)
	}
	return ctx.Redirect(start.URL, fiber.StatusFound)
}

// Callback is where providers send the browser back.
func (c Controller) Callback(ctx *fiber.Ctx) error {
	cookie := ctx.Cookies(application.SSOCookie)
	ctx.Cookie(&fiber.Cookie{Name: application.SSOCookie, Value: "", Path: "/api/v1/auth/sso/", MaxAge: -1, Expires: time.Unix(0, 0),
		HTTPOnly: true, Secure: c.secure(ctx), SameSite: fiber.CookieSameSiteLaxMode})
	ip := c.Limits.ClientIP(ctx)
	if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
		return c.fail(ctx, "/login", errors.New("too many sign-in attempts; wait "+r.Reset.Round(time.Second).String()))
	}
	out, err := c.SSO.Callback(ctx.UserContext(), ctx.Params("provider"), ctx.Query("state"), ctx.Query("code"), ctx.Query("error"), cookie, ctx.Get("User-Agent"), ip)
	if err != nil {
		if errors.Is(err, application.ErrSSOFlow) {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		return c.fail(ctx, "/login", err)
	}
	return c.outcome(ctx, out)
}

// outcome sends the browser on after a sign-in: signed in (cookies), to
// the 2FA step, to the link page, or back (links and sudo).
func (c Controller) outcome(ctx *fiber.Ctx, out *application.SSOOutcome) error {
	switch {
	case out.Login != nil:
		expires := time.Now().Add(time.Duration(c.Config.Session.ExpirationMinutes) * time.Minute)
		if err := websession.Set(ctx, out.Login.Token, expires, c.secure(ctx)); err != nil {
			return err
		}
		return ctx.Redirect(out.Redirect, fiber.StatusFound)
	case out.Challenge != "":
		q := url.Values{"challenge": {out.Challenge}, "methods": {strings.Join(out.Methods, ",")}, "redirect": {out.Redirect}}
		return ctx.Redirect("/login?"+q.Encode(), fiber.StatusFound)
	case out.LinkToken != "":
		// In the fragment: never sent to a server, nor in a Referer.
		return ctx.Redirect("/login/link#token="+out.LinkToken, fiber.StatusFound)
	}
	return ctx.Redirect(out.Redirect, fiber.StatusFound)
}

// ACS takes a SAML identity provider's POST.
func (c Controller) ACS(ctx *fiber.Ctx) error {
	ip := c.Limits.ClientIP(ctx)
	if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
		return c.fail(ctx, "/login", errors.New("too many sign-in attempts; wait "+r.Reset.Round(time.Second).String()))
	}
	next, out, err := c.SSO.ACS(ctx.UserContext(), ctx.Params("provider"), ctx.FormValue("SAMLResponse"), ctx.FormValue("RelayState"), ctx.Get("User-Agent"), ip)
	if err != nil {
		c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		return c.fail(ctx, "/login", err)
	}
	if next != "" {
		// A top-level GET: the flow's cookie comes along.
		return ctx.Redirect(next, fiber.StatusSeeOther)
	}
	return c.outcome(ctx, out)
}

// Finish completes a SAML sign-in in the browser that started it.
func (c Controller) Finish(ctx *fiber.Ctx) error {
	cookie := ctx.Cookies(application.SSOCookie)
	ctx.Cookie(&fiber.Cookie{Name: application.SSOCookie, Value: "", Path: "/api/v1/auth/sso/", MaxAge: -1, Expires: time.Unix(0, 0),
		HTTPOnly: true, Secure: c.secure(ctx), SameSite: fiber.CookieSameSiteLaxMode})
	ip := c.Limits.ClientIP(ctx)
	out, err := c.SSO.Finish(ctx.Params("provider"), ctx.Query("state"), cookie, ctx.Get("User-Agent"), ip)
	if err != nil {
		c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		return c.fail(ctx, "/login", err)
	}
	return c.outcome(ctx, out)
}

// Metadata is this server's SAML SP metadata, for the identity provider.
func (c Controller) Metadata(ctx *fiber.Ctx) error {
	xml, err := c.SSO.SPMetadata(ctx.UserContext(), ctx.Params("provider"))
	if err != nil {
		return ctx.Status(fiber.StatusNotFound).SendString("not found")
	}
	ctx.Set(fiber.HeaderContentType, "application/samlmetadata+xml")
	return ctx.Send(xml)
}

type Provider struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type ProvidersResponse struct {
	Providers []Provider `json:"providers"`
}

func (c Controller) Providers(ctx *fiber.Ctx, _ struct{}) (*ProvidersResponse, *fiberoapi.ErrorResponse) {
	ps, err := c.SSO.Enabled()
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := &ProvidersResponse{Providers: []Provider{}}
	for _, p := range ps {
		if p.Kind == domain.SSOKindLDAP {
			continue // the sign-in form
		}
		out.Providers = append(out.Providers, Provider{Slug: p.Slug, Name: p.Name, Kind: p.Kind})
	}
	return out, nil
}

type LinkInfoRequest struct {
	Token string `query:"token" validate:"required"`
}

type LinkInfoResponse struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

func (c Controller) LinkInfo(ctx *fiber.Ctx, req LinkInfoRequest) (*LinkInfoResponse, *fiberoapi.ErrorResponse) {
	info, err := c.SSO.LinkInfo(req.Token)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &LinkInfoResponse{Provider: info.Provider, Email: info.Email, Username: info.Username}, nil
}

type CompleteLinkRequest struct {
	Token    string `json:"token" validate:"required"`
	Password string `json:"password" validate:"required"`
	Code     string `json:"code"`
}

type CompleteLinkResponse struct {
	Redirect  string    `json:"redirect"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (c Controller) CompleteLink(ctx *fiber.Ctx, req CompleteLinkRequest) (*CompleteLinkResponse, *fiberoapi.ErrorResponse) {
	ip := c.Limits.ClientIP(ctx)
	account := ""
	if u, err := c.SSO.LinkUser(req.Token); err == nil {
		account = u.Email
	}
	if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, account); blocked {
		return nil, ratelimit.Refused(ctx, r)
	}
	login, redirect, err := c.SSO.CompleteLink(req.Token, req.Password, req.Code, ctx.Get("User-Agent"), ip)
	if errors.Is(err, application.ErrInvalidCredentials) || errors.Is(err, application.ErrInvalidCode) {
		c.Limits.LoginFailed(ctx.UserContext(), ip, account)
	}
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	expires := time.Now().Add(time.Duration(c.Config.Session.ExpirationMinutes) * time.Minute)
	if err := websession.Set(ctx, login.Token, expires, c.secure(ctx)); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &CompleteLinkResponse{Redirect: redirect, ExpiresAt: expires}, nil
}

type Identity struct {
	Id           string     `json:"id"`
	Provider     string     `json:"provider"`
	ProviderName string     `json:"provider_name"`
	Email        string     `json:"email"`
	Username     string     `json:"username"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

type IdentitiesResponse struct {
	Identities []Identity `json:"identities"`
}

func (c Controller) Identities(ctx *fiber.Ctx, _ struct{}) (*IdentitiesResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	ids, err := c.SSO.Identities(auth.UserID)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := &IdentitiesResponse{Identities: []Identity{}}
	for _, i := range ids {
		out.Identities = append(out.Identities, Identity{Id: i.Id, Provider: i.Provider, ProviderName: i.ProviderName, Email: i.Email,
			Username: i.Username, LastLoginAt: i.LastLoginAt, CreatedAt: i.CreatedAt})
	}
	return out, nil
}

type IdentityRequest struct {
	Id string `path:"id" json:"-" validate:"required"`
}

type Message struct {
	Message string `json:"message"`
}

func (c Controller) Unlink(ctx *fiber.Ctx, req IdentityRequest) (*Message, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := c.SSO.Unlink(auth, req.Id); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Message{Message: "unlinked"}, nil
}

// AdminProvider is a provider for admins (never the secret).
type AdminProvider struct {
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	Issuer         string `json:"issuer"`
	ClientId       string `json:"client_id"`
	HasSecret      bool   `json:"has_secret"`
	Scopes         string `json:"scopes"`
	AllowedDomains string `json:"allowed_domains"`
	AllowedOrgs    string `json:"allowed_orgs"`
	// SAML (kind saml): issuer is the IdP's metadata URL; metadata_url is
	// this SP's (entity id) for the IdP, acs_url where it posts.
	SAML             *domain.SAMLSettings `json:"saml,omitempty"`
	MetadataURL      string               `json:"metadata_url,omitempty"`
	ACSURL           string               `json:"acs_url,omitempty"`
	AutoCreate       bool                 `json:"auto_create"`
	RequireTwoFactor bool                 `json:"require_two_factor"`
	Enabled          bool                 `json:"enabled"`
	FromConfig       bool                 `json:"from_config"`
	CallbackURL      string               `json:"callback_url"`
	// Group sync: groups_claim (OIDC, default "groups"), admin_group,
	// group_mappings.
	GroupsClaim   string                   `json:"groups_claim"`
	AdminGroup    string                   `json:"admin_group"`
	GroupMappings []domain.SSOGroupMapping `json:"group_mappings"`
	// LDAP (kind ldap): issuer is the URL, client_id the service account's
	// DN, client_secret its password.
	LDAP *domain.LDAPSettings `json:"ldap,omitempty"`
}

func (c Controller) toAdmin(p *domain.SSOProvider) AdminProvider {
	out := AdminProvider{Slug: p.Slug, Name: p.Name, Kind: p.Kind, Issuer: p.Issuer, ClientId: p.ClientId, HasSecret: p.ClientSecret != "",
		Scopes: p.Scopes, AllowedDomains: p.AllowedDomains, AllowedOrgs: p.AllowedOrgs, AutoCreate: p.AutoCreate, RequireTwoFactor: p.RequireTwoFactor,
		Enabled: p.Enabled, FromConfig: p.FromConfig, CallbackURL: c.SSO.CallbackURL(p.Slug), GroupsClaim: p.GroupsClaim, AdminGroup: p.AdminGroup,
		GroupMappings: application.Mappings(p), LDAP: application.DirectorySettings(p), SAML: application.SAMLSettingsOf(p)}
	if p.Kind == domain.SSOKindSAML && out.CallbackURL != "" {
		base := strings.TrimSuffix(out.CallbackURL, "/callback")
		out.MetadataURL, out.ACSURL, out.CallbackURL = base+"/metadata", base+"/acs", ""
	}
	return out
}

type AdminProvidersResponse struct {
	Providers []AdminProvider `json:"providers"`
}

func (c Controller) AdminProviders(ctx *fiber.Ctx, _ struct{}) (*AdminProvidersResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	ps, err := c.SSO.AdminProviders(auth.UserID)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := &AdminProvidersResponse{Providers: []AdminProvider{}}
	for i := range ps {
		out.Providers = append(out.Providers, c.toAdmin(&ps[i]))
	}
	return out, nil
}

type ProviderFields struct {
	Name             *string `json:"name"`
	Kind             *string `json:"kind"`
	Issuer           *string `json:"issuer"`
	ClientId         *string `json:"client_id"`
	ClientSecret     *string `json:"client_secret"` // write-only
	Scopes           *string `json:"scopes"`
	AllowedDomains   *string `json:"allowed_domains"`
	AllowedOrgs      *string `json:"allowed_orgs"`
	AutoCreate       *bool   `json:"auto_create"`
	RequireTwoFactor *bool   `json:"require_two_factor"`
	Enabled          *bool   `json:"enabled"`
	GroupsClaim      *string `json:"groups_claim"`
	AdminGroup       *string `json:"admin_group"`
	// GroupMappings: [{group: <provider's>, target: <oGit group path>,
	// role: member|maintainer}]; replaces them all.
	GroupMappings *[]domain.SSOGroupMapping `json:"group_mappings"`
	LDAP          *domain.LDAPSettings      `json:"ldap"`
	SAML          *domain.SAMLSettings      `json:"saml"`
}

func (f ProviderFields) input() application.ProviderInput {
	return application.ProviderInput{Name: f.Name, Kind: f.Kind, Issuer: f.Issuer, ClientId: f.ClientId, ClientSecret: f.ClientSecret,
		Scopes: f.Scopes, AllowedDomains: f.AllowedDomains, AllowedOrgs: f.AllowedOrgs, AutoCreate: f.AutoCreate,
		RequireTwoFactor: f.RequireTwoFactor, Enabled: f.Enabled, GroupsClaim: f.GroupsClaim, AdminGroup: f.AdminGroup, GroupMappings: f.GroupMappings, LDAP: f.LDAP, SAML: f.SAML}
}

type CreateProviderRequest struct {
	Slug string `json:"slug" validate:"required"`
	ProviderFields
}

type ProviderRequest struct {
	Slug string `path:"slug" json:"-" validate:"required"`
}

type UpdateProviderRequest struct {
	Slug string `path:"slug" json:"-" validate:"required"`
	ProviderFields
}

func (c Controller) CreateProvider(ctx *fiber.Ctx, req CreateProviderRequest) (*AdminProvider, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	in := req.ProviderFields.input()
	in.Slug = &req.Slug
	p, err := c.SSO.CreateProvider(auth, in)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := c.toAdmin(p)
	return &out, nil
}

func (c Controller) UpdateProvider(ctx *fiber.Ctx, req UpdateProviderRequest) (*AdminProvider, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	p, err := c.SSO.UpdateProvider(auth, req.Slug, req.ProviderFields.input())
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := c.toAdmin(p)
	return &out, nil
}

func (c Controller) DeleteProvider(ctx *fiber.Ctx, req ProviderRequest) (*Message, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := c.SSO.DeleteProvider(auth, req.Slug); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Message{Message: "removed"}, nil
}
