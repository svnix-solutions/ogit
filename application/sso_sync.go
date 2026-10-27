package application

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2/utils"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
)

// Group and admin sync, and signing in only through providers:
//   - at every sign-in through a provider (and when a pending link is
//     confirmed), the provider's groups (groups_claim; GitHub: the user's
//     organizations) are synced: members of admin_group become site admins,
//     and group_mappings add them to oGit groups as member or maintainer
//     (the highest role among their mapped groups);
//   - sync only takes back what it gave: memberships it added (synced_from)
//     are updated or removed when the user leaves the IdP group; ones added
//     by hand are never touched. Site admin is revoked only from admins it
//     granted (role_synced; a manual change clears it), and never from the
//     last active admin;
//   - sync failures are logged; the sign-in goes on;
//   - signing in only through providers (security.sso_only, or the admin
//     page with an enabled provider, and only when the admin turning it on
//     keeps a way in) turns off password and passkey sign-in, password
//     sudo, first passwords and registration for every user but the
//     configuration's security.sso_only_exempt. Tokens and SSH keys keep
//     working. Their passwords then don't count against trusting the
//     provider's MFA (Trusted).

var (
	ErrPasswordLoginDisabled = errors.New("sign in through your organization's provider: password sign-in is turned off on this server")
	ErrSSOOnlyNoProvider     = errors.New("add and enable a sign-in provider first")
	ErrSSOOnlyLockout        = errors.New("you would be locked out: link a sign-in provider to your account first, or add your user name to security.sso_only_exempt")
	ErrInvalidGroupMapping   = errors.New("invalid group mapping: each needs the provider's group, an oGit group, and the role member or maintainer")
)

// SSOOnly reports whether sign-in goes only through providers, and
// whether the configuration forces it.
func (c *SSOApp) SSOOnly() (on, forced bool, err error) {
	if c == nil {
		return false, false, nil
	}
	if c.Config.Security.SSOOnly {
		return true, true, nil
	}
	if c.Settings == nil {
		return false, false, nil
	}
	v, err := c.Settings.Get(domain.SettingSSOOnly)
	if err != nil {
		return false, false, err
	}
	on, _ = strconv.ParseBool(v)
	return on, false, nil
}

// exempt: the configuration's break-glass accounts.
func (c *SSOApp) exempt(username string) bool {
	return slices.ContainsFunc(c.Config.Security.SSOOnlyExempt, func(u string) bool { return strings.EqualFold(strings.TrimSpace(u), username) })
}

// PasswordBlocked reports whether user may not sign in (or confirm sudo)
// with a password or passkey.
func (c *SSOApp) PasswordBlocked(user *domain.User) bool {
	on, _, err := c.SSOOnly()
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.sso").Msg("cannot read sso_only")
		return false
	}
	return on && !c.exempt(user.Username)
}

// passwordUsable: the user has a password they may sign in with.
func (c *SSOApp) passwordUsable(user *domain.User) bool {
	return user.Password != "" && !c.PasswordBlocked(user)
}

// SetSSOOnly turns sign-in only through providers on or off (admins,
// sudo).
func (c *SSOApp) SetSSOOnly(auth *fiberoapi.AuthContext, on bool) error {
	if err := c.adminCheck(auth); err != nil {
		return err
	}
	if on {
		enabled, err := c.Enabled()
		if err != nil {
			return err
		}
		if len(enabled) == 0 {
			return ErrSSOOnlyNoProvider
		}
		user, err := c.Users.GetById(auth.UserID)
		if err != nil {
			return err
		}
		if !c.exempt(user.Username) {
			ids, err := c.Pers.Identities(user.Id)
			if err != nil {
				return err
			}
			linked := false
			for _, i := range ids {
				for _, p := range enabled {
					linked = linked || p.Slug == i.Provider
				}
			}
			if !linked {
				return ErrSSOOnlyLockout
			}
		}
	}
	if err := c.Settings.Set(domain.SettingSSOOnly, strconv.FormatBool(on)); err != nil {
		return err
	}
	c.audit(auth.UserID, "site.sso_only").Bool("on", on).Msg("sign-in only through providers changed")
	return nil
}

// Mappings parses a provider's group mappings.
func Mappings(p *domain.SSOProvider) []domain.SSOGroupMapping {
	out := []domain.SSOGroupMapping{}
	if p.GroupMappings != "" {
		_ = json.Unmarshal([]byte(p.GroupMappings), &out)
	}
	return out
}

// encodeMappings checks and stores mappings.
func encodeMappings(ms []domain.SSOGroupMapping) (string, error) {
	for i := range ms {
		ms[i].Group, ms[i].Target = strings.TrimSpace(ms[i].Group), strings.Trim(strings.TrimSpace(ms[i].Target), "/")
		if ms[i].Group == "" || ms[i].Target == "" || (ms[i].Role != domain.GroupRoleMember && ms[i].Role != domain.GroupRoleMaintainer) {
			return "", ErrInvalidGroupMapping
		}
	}
	if ms == nil {
		ms = []domain.SSOGroupMapping{}
	}
	b, err := json.Marshal(ms)
	return string(b), err
}

func roleRank(r domain.GroupRole) int {
	switch r {
	case domain.GroupRoleOwner:
		return 3
	case domain.GroupRoleMaintainer:
		return 2
	case domain.GroupRoleMember:
		return 1
	}
	return 0
}

// syncGroups applies a provider's admin group and group mappings to user
// (errors are logged).
func (c *SSOApp) syncGroups(p *domain.SSOProvider, user *domain.User, groups []string) {
	log := func(err error, what string) {
		c.Logger.Error().Err(err).Str("component", "application.sso").Str("provider", p.Slug).Str("user_id", user.Id).Msg(what)
	}
	in := func(g string) bool { return slices.Contains(groups, g) }

	if p.AdminGroup != "" {
		switch {
		case in(p.AdminGroup) && user.Role != domain.RoleAdmin:
			if err := c.Users.SetSyncedRole(user.Id, domain.RoleAdmin); err != nil {
				log(err, "admin sync failed")
			} else {
				c.audit(user.Id, "sso.sync.admin").Str("provider", p.Slug).Bool("admin", true).Msg("site admin granted by the provider's group")
			}
		case !in(p.AdminGroup) && user.Role == domain.RoleAdmin && user.RoleSynced:
			admins, err := c.Users.CountActiveAdmins()
			if err != nil {
				log(err, "admin sync failed")
			} else if admins <= 1 {
				c.Logger.Warn().Str("component", "application.sso").Str("user_id", user.Id).Msg("kept the last active admin despite the provider's group")
			} else if err := c.Users.SetSyncedRole(user.Id, domain.RoleUser); err != nil {
				log(err, "admin sync failed")
			} else {
				c.audit(user.Id, "sso.sync.admin").Str("provider", p.Slug).Bool("admin", false).Msg("site admin revoked by the provider's group")
			}
		}
	}

	ms := Mappings(p)
	if len(ms) == 0 || c.Groups == nil {
		return
	}
	// The role each mapped group should give (the highest), or none.
	want := map[string]domain.GroupRole{}
	for _, m := range ms {
		if _, ok := want[m.Target]; !ok {
			want[m.Target] = ""
		}
		if in(m.Group) && roleRank(m.Role) > roleRank(want[m.Target]) {
			want[m.Target] = m.Role
		}
	}
	for target, role := range want {
		g, err := c.Groups.GetByName(target)
		if err != nil {
			c.Logger.Warn().Str("component", "application.sso").Str("provider", p.Slug).Str("group", target).Msg("a mapped group doesn't exist")
			continue
		}
		member, err := c.Groups.GetMember(g.Id, user.Id)
		if isNotFound(err) {
			member = nil
		} else if err != nil {
			log(err, "group sync failed")
			continue
		}
		synced := member != nil && member.SyncedFrom != nil && *member.SyncedFrom == p.Slug
		switch {
		case role != "" && member == nil:
			slug := p.Slug
			err = c.Groups.AddMember(&domain.GroupMember{Id: utils.UUIDv4(), GroupId: g.Id, UserId: user.Id, Role: role, SyncedFrom: &slug})
		case role != "" && synced && member.Role != role:
			err = c.Groups.UpdateMemberRole(g.Id, user.Id, role)
		case role == "" && synced:
			err = c.Groups.RemoveMember(g.Id, user.Id)
		default:
			continue // up to date, or added by hand
		}
		if err != nil {
			log(err, "group sync failed")
			continue
		}
		c.audit(user.Id, "sso.sync.group").Str("provider", p.Slug).Str("group", target).Str("role", string(role)).Msg("group membership synced")
	}
}
