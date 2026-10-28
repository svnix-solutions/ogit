// Package sso signs users in through OpenID Connect providers and GitHub
// (OAuth2): authorization URLs with PKCE (and a nonce for OIDC), code
// exchange, and the verified claims.
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Claims is who signed in.
type Claims struct {
	Subject       string // stable id at the provider
	Email         string
	EmailVerified bool
	Username      string // preferred_username, GitHub's login
	Name          string
	Groups        []string
}

// Endpoint configures a client.
type Endpoint struct {
	Kind         string // "oidc" or "github"
	Issuer       string // OIDC issuer; GitHub's base URL
	ClientId     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string // extra
	AllowedOrgs  []string // GitHub
	// GroupsClaim (OIDC): where the groups are ("groups" when empty; a
	// dotted path reaches nested claims, e.g. realm_access.roles).
	GroupsClaim string
}

// Client is a configured provider.
type Client interface {
	// AuthURL is where to send the browser.
	AuthURL(state, nonce, verifier string) string
	// Exchange turns the callback's code into verified claims.
	Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error)
}

var (
	ErrNotAllowed = errors.New("your account at the provider isn't allowed to sign in here")
	ErrNoSubject  = errors.New("the provider didn't say who signed in")
)

// New connects to a provider (OIDC: its discovery document).
func New(ctx context.Context, e Endpoint, hc *http.Client) (Client, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	switch e.Kind {
	case "oidc":
		return newOIDC(ctx, e, hc)
	case "github":
		return newGitHub(e, hc), nil
	}
	return nil, fmt.Errorf("unknown provider kind %q", e.Kind)
}

// GenerateVerifier is a PKCE code verifier.
func GenerateVerifier() string { return oauth2.GenerateVerifier() }

type oidcClient struct {
	hc          *http.Client
	provider    *oidc.Provider
	config      oauth2.Config
	verifier    *oidc.IDTokenVerifier
	groupsClaim string
}

func newOIDC(ctx context.Context, e Endpoint, hc *http.Client) (*oidcClient, error) {
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, hc), strings.TrimSuffix(e.Issuer, "/"))
	if err != nil {
		return nil, fmt.Errorf("reading the provider's OpenID configuration: %w", err)
	}
	scopes := append([]string{oidc.ScopeOpenID, "profile", "email"}, e.Scopes...)
	groups := e.GroupsClaim
	if groups == "" {
		groups = "groups"
	}
	return &oidcClient{hc: hc, provider: provider, groupsClaim: groups,
		config:   oauth2.Config{ClientID: e.ClientId, ClientSecret: e.ClientSecret, Endpoint: provider.Endpoint(), RedirectURL: e.RedirectURL, Scopes: dedupe(scopes)},
		verifier: provider.Verifier(&oidc.Config{ClientID: e.ClientId})}, nil
}

func (c *oidcClient) AuthURL(state, nonce, verifier string) string {
	return c.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// oidcClaims: email_verified is a string at some providers.
type oidcClaims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     any    `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Nickname          string `json:"nickname"`
	Name              string `json:"name"`
}

// GroupsAt reads the groups at a dotted path of raw claims: a list of
// strings, or one string.
func GroupsAt(raw map[string]any, path string) []string {
	var v any = raw
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[part]
	}
	var out []string
	switch g := v.(type) {
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = []string{g}
	}
	return out
}

func (o oidcClaims) claims() Claims {
	c := Claims{Subject: o.Subject, Email: o.Email, Username: o.PreferredUsername, Name: o.Name}
	if c.Username == "" {
		c.Username = o.Nickname
	}
	switch v := o.EmailVerified.(type) {
	case bool:
		c.EmailVerified = v
	case string:
		c.EmailVerified, _ = strconv.ParseBool(v)
	}
	return c
}

func (c *oidcClient) Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error) {
	ctx = oidc.ClientContext(ctx, c.hc)
	tok, err := c.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchanging the code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return nil, errors.New("the provider sent no ID token")
	}
	idt, err := c.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verifying the ID token: %w", err)
	}
	if idt.Nonce != nonce {
		return nil, errors.New("the ID token's nonce doesn't match")
	}
	var oc oidcClaims
	if err := idt.Claims(&oc); err != nil {
		return nil, err
	}
	var all map[string]any
	if err := idt.Claims(&all); err != nil {
		return nil, err
	}
	claims := oc.claims()
	claims.Groups = GroupsAt(all, c.groupsClaim)
	if claims.Email == "" || claims.Username == "" { // some providers keep them for userinfo
		if info, err := c.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil && info.Subject == claims.Subject {
			var more oidcClaims
			var moreRaw map[string]any
			if info.Claims(&more) == nil && info.Claims(&moreRaw) == nil {
				extra := more.claims()
				extra.Groups = GroupsAt(moreRaw, c.groupsClaim)
				if claims.Email == "" {
					claims.Email, claims.EmailVerified = extra.Email, extra.EmailVerified
				}
				if claims.Username == "" {
					claims.Username = extra.Username
				}
				if len(claims.Groups) == 0 {
					claims.Groups = extra.Groups
				}
			}
		}
	}
	if claims.Subject == "" {
		return nil, ErrNoSubject
	}
	return &claims, nil
}

type githubClient struct {
	hc     *http.Client
	config oauth2.Config
	api    string
	orgs   []string
}

func newGitHub(e Endpoint, hc *http.Client) *githubClient {
	base := strings.TrimSuffix(e.Issuer, "/")
	if base == "" {
		base = "https://github.com"
	}
	api := base + "/api/v3"
	if base == "https://github.com" {
		api = "https://api.github.com"
	}
	scopes := []string{"read:user", "user:email"}
	if len(e.AllowedOrgs) > 0 {
		scopes = append(scopes, "read:org")
	}
	return &githubClient{hc: hc, api: api, orgs: e.AllowedOrgs, config: oauth2.Config{ClientID: e.ClientId, ClientSecret: e.ClientSecret,
		Endpoint:    oauth2.Endpoint{AuthURL: base + "/login/oauth/authorize", TokenURL: base + "/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL: e.RedirectURL, Scopes: dedupe(append(scopes, e.Scopes...))}}
}

func (c *githubClient) AuthURL(state, _, verifier string) string {
	return c.config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
}

func (c *githubClient) get(ctx context.Context, tok *oauth2.Token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.api+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub answered %s for %s", resp.Status, path)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (c *githubClient) Exchange(ctx context.Context, code, verifier, _ string) (*Claims, error) {
	tok, err := c.config.Exchange(context.WithValue(ctx, oauth2.HTTPClient, c.hc), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchanging the code: %w", err)
	}
	var u struct {
		Id    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := c.get(ctx, tok, "/user", &u); err != nil {
		return nil, err
	}
	if u.Id == 0 {
		return nil, ErrNoSubject
	}
	claims := &Claims{Subject: strconv.FormatInt(u.Id, 10), Username: u.Login, Name: u.Name}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := c.get(ctx, tok, "/user/emails", &emails); err == nil {
		for _, e := range emails {
			if e.Primary {
				claims.Email, claims.EmailVerified = e.Email, e.Verified
			}
		}
	}
	if len(c.orgs) > 0 {
		var orgs []struct {
			Login string `json:"login"`
		}
		if err := c.get(ctx, tok, "/user/orgs", &orgs); err != nil {
			return nil, err
		}
		allowed := false
		for _, o := range orgs {
			for _, want := range c.orgs {
				allowed = allowed || strings.EqualFold(o.Login, want)
			}
			claims.Groups = append(claims.Groups, o.Login)
		}
		if !allowed {
			return nil, ErrNotAllowed
		}
	}
	return claims, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
