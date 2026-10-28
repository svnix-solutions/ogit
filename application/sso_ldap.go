package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/sso"
)

// Directory (LDAP / Active Directory) rules:
//   - a directory is a provider of kind ldap: the sign-in form tries every
//     enabled directory first (the input is a user name or an email, found
//     with user_filter, then bound as that user), then local accounts, so
//     break-glass accounts keep working when a directory is down; the
//     answer is the same for unknown users and wrong passwords;
//   - a directory sign-in follows the other providers' rules (identity,
//     link by verified email after the existing account's password,
//     auto-creation, allowed domains, group sync), but it is one factor:
//     oGit 2FA is asked when the user has it, and it never counts as the
//     provider's MFA;
//   - a directory password confirms sudo too (also when only providers sign
//     in), and makes the account's password sign-in work in the UI's eyes
//     (PasswordLogin); git over HTTP still takes tokens only;
//   - the service account (client_id, client_secret) is optional
//     (anonymous search); its password is sealed like client secrets.

// directory returns the client of a directory provider.
func (c *SSOApp) directory(p *domain.SSOProvider) (sso.LDAP, error) {
	d := sso.LDAP{URL: p.Issuer, BindDN: p.ClientId}
	if p.LDAPConfig != "" {
		if err := json.Unmarshal([]byte(p.LDAPConfig), &d.Settings); err != nil {
			return d, err
		}
	}
	secret := p.ClientSecret
	if !p.FromConfig && secret != "" {
		if c.Box == nil {
			return d, ErrMirrorNeedsKey
		}
		plain, err := c.Box.Open(secret)
		if err != nil {
			return d, err
		}
		secret = plain
	}
	d.BindPassword = secret
	return d, nil
}

// directories lists the enabled directory providers.
func (c *SSOApp) directories() []domain.SSOProvider {
	if c == nil {
		return nil
	}
	all, err := c.providers()
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.sso").Msg("cannot list directories")
		return nil
	}
	var out []domain.SSOProvider
	for _, p := range all {
		if p.Kind == domain.SSOKindLDAP && p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// HasDirectory reports whether the sign-in form takes directory names.
func (c *SSOApp) HasDirectory() bool { return len(c.directories()) > 0 }

// SignInWithDirectory tries the sign-in form's credentials on every
// directory; handled is false when none knows them (the local accounts are
// tried next).
func (c *SSOApp) SignInWithDirectory(ctx context.Context, input, password, userAgent, ip string) (out *SSOOutcome, handled bool, err error) {
	for _, p := range c.directories() {
		d, err := c.directory(&p)
		if err != nil {
			c.Logger.Error().Err(err).Str("component", "application.sso").Str("provider", p.Slug).Msg("directory misconfigured")
			continue
		}
		claims, err := d.Authenticate(ctx, input, password)
		if errors.Is(err, sso.ErrLDAPInvalidCredentials) {
			continue
		} else if err != nil {
			c.Logger.Warn().Err(err).Str("component", "application.sso").Str("provider", p.Slug).Msg("directory unavailable")
			continue
		}
		if !domainAllowed(&p, claims) {
			return nil, true, ErrSSONotAllowed
		}
		identity, err := c.Pers.Identity(p.Slug, claims.Subject)
		if err != nil && !isNotFound(err) {
			return nil, true, err
		}
		out, err := c.completeLogin(&SSOOutcome{Redirect: "/"}, &p, identity, claims, userAgent, ip)
		return out, true, err
	}
	return nil, false, nil
}

// DirectoryPassword reports whether password is user's password in a
// directory they are linked to (sudo).
func (c *SSOApp) DirectoryPassword(user *domain.User, password string) bool {
	if c == nil || password == "" {
		return false
	}
	ids, err := c.Pers.Identities(user.Id)
	if err != nil {
		return false
	}
	for _, p := range c.directories() {
		for _, i := range ids {
			if i.Provider != p.Slug {
				continue
			}
			d, err := c.directory(&p)
			if err != nil {
				continue
			}
			login := i.Username
			if login == "" {
				login = i.Email
			}
			if claims, err := d.Authenticate(context.Background(), login, password); err == nil && claims.Subject == i.Subject {
				return true
			}
		}
	}
	return false
}

// PasswordLogin reports whether a password signs user in (and confirms
// sudo): their own, or a linked directory's.
func (c *SSOApp) PasswordLogin(user *domain.User) bool {
	if c.passwordUsable(user) {
		return true
	}
	ids, err := c.Pers.Identities(user.Id)
	if err != nil {
		return false
	}
	for _, p := range c.directories() {
		for _, i := range ids {
			if i.Provider == p.Slug {
				return true
			}
		}
	}
	return false
}

// DirectorySettings are a directory provider's settings (nil for other
// kinds).
func DirectorySettings(p *domain.SSOProvider) *domain.LDAPSettings {
	if p.Kind != domain.SSOKindLDAP {
		return nil
	}
	var s domain.LDAPSettings
	_ = json.Unmarshal([]byte(p.LDAPConfig), &s)
	return &s
}

// checkDirectory validates a directory provider's settings and stores
// them.
func checkDirectory(p *domain.SSOProvider, s *domain.LDAPSettings) error {
	var current domain.LDAPSettings
	if p.LDAPConfig != "" {
		_ = json.Unmarshal([]byte(p.LDAPConfig), &current)
	}
	if s != nil {
		current = *s
	}
	current.UserBase, current.UserFilter = strings.TrimSpace(current.UserBase), strings.TrimSpace(current.UserFilter)
	if err := sso.CheckLDAP(p.Issuer, current); err != nil {
		return errors.Join(ErrInvalidSSOProvider, err)
	}
	b, err := json.Marshal(current)
	if err != nil {
		return err
	}
	p.LDAPConfig = string(b)
	return nil
}
