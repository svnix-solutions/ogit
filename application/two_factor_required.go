package application

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/labbs/ogit/domain"
)

// Requiring two-factor authentication:
//   - site-wide: security.require_2fa in the configuration forces it on;
//     otherwise site admins switch it from the admin page (audit-logged).
//     Users without 2FA can then sign in, but only reach their 2FA setup
//     (and sign out): the API answers TWO_FACTOR_REQUIRED, and their access
//     tokens and SSH keys are refused for git and the API. Repository tokens
//     and deploy keys are not people and keep working;
//   - site admins need 2FA for admin actions (ADMIN_TWO_FACTOR_REQUIRED);
//     `ogit admin` on the server is not affected;
//   - per group (its owner decides): members without 2FA get no access to
//     the group's repositories through the group (its namespace or its
//     repository memberships) and can't create repositories in it. They
//     stay members; owners and maintainers see who lacks 2FA.

var (
	ErrTwoFactorRequired      = errors.New("two-factor authentication is required on this server: set it up in your settings first")
	ErrAdminNeedsTwoFactor    = errors.New("site administration needs two-factor authentication: set it up in your settings first")
	ErrGroupRequiresTwoFactor = errors.New("this group requires two-factor authentication: set it up in your settings first")
	// ErrGitTwoFactorRequired refuses git for users who must set up 2FA.
	ErrGitTwoFactorRequired = fmt.Errorf("%w: two-factor authentication is required on this server; set it up in your settings", ErrGitForbidden)
)

// SiteRequirement reports whether every user must have 2FA, and whether
// the configuration forces it (the admin toggle can't turn it off then).
func (c *TwoFactorApp) SiteRequirement() (required, forced bool, err error) {
	if c.Config.Security.RequireTwoFactor {
		return true, true, nil
	}
	if c.Settings == nil {
		return false, false, nil
	}
	v, err := c.Settings.Get(domain.SettingRequireTwoFactor)
	if err != nil {
		return false, false, err
	}
	on, _ := strconv.ParseBool(v)
	return on, false, nil
}

// MustEnroll reports whether userId must set up 2FA before anything else:
// the site requires it and they have none. Only people (not repository
// token principals) are concerned.
func (c *TwoFactorApp) MustEnroll(userId string) (bool, error) {
	if c == nil || !domain.IsPerson(userId) {
		return false, nil
	}
	required, _, err := c.SiteRequirement()
	if err != nil || !required {
		return false, err
	}
	on, err := c.Satisfied(userId)
	return !on, err
}

// Satisfied reports whether userId counts as having two-factor
// authentication for requirements: oGit's 2FA, or signing in only through
// providers whose MFA is trusted (IdPTrusted, application/sso_app.go).
func (c *TwoFactorApp) Satisfied(userId string) (bool, error) {
	on, err := c.Enabled(userId)
	if err != nil || on || c.IdPTrusted == nil {
		return on, err
	}
	return c.IdPTrusted(userId)
}

// SessionTrusted reports whether a session was opened through a provider
// whose MFA is trusted.
func (c *TwoFactorApp) SessionTrusted(sessionId string) bool {
	if sessionId == "" {
		return false
	}
	s, err := c.Sessions.GetById(sessionId)
	return err == nil && s.SSOProvider != nil && *s.SSOProvider != ""
}

// SetSiteRequirement stores the admin toggle.
func (c *TwoFactorApp) SetSiteRequirement(on bool) error {
	return c.Settings.Set(domain.SettingRequireTwoFactor, strconv.FormatBool(on))
}
