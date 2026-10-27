package application

import (
	"github.com/labbs/ogit/domain"
)

// Sub-group rules:
//   - a group may have sub-groups, at most domain.MaxGroupDepth levels in
//     all (org/a/b/c/d); a group's name is its full path, so a sub-group's
//     path is its parent's plus its own segment, and repositories live
//     under any group: org/team/app;
//   - membership is inherited down the tree: a member of a group has the
//     same role in every sub-group below it; a sub-group adds its own
//     members, or raises an inherited member's role, never lowers it; a
//     user's role in a group is the highest of their memberships in it and
//     its ancestors;
//   - every group-based decision goes through groupRoles: namespace roles
//     on repositories, groups granted on repositories (their inherited
//     members count), branch and tag allowlists, managing groups;
//   - two-factor requirements are inherited too: for repository access,
//     users without 2FA get no role in a group that requires it or whose
//     ancestor does (a sub-group can add the requirement, not drop it);
//   - a sub-group's path must not be a repository's path (org/team the
//     group vs org/team the repository), and vice versa.

// groupRoles answers users' roles in groups, inherited down the tree.
type groupRoles struct {
	groups domain.GroupPers
	// twoFactor tells whether a user has 2FA, to apply the groups'
	// requirements (nil: they don't apply, for managing groups).
	twoFactor func(userId string) (bool, error)
}

// requires reports whether a group (by path) or one of its ancestors
// requires 2FA, given the require flags of those groups by path.
func requires(path string, flags map[string]bool) bool {
	for _, p := range domain.GroupPathPrefixes(path) {
		if flags[p] {
			return true
		}
	}
	return false
}

// blocked reports whether the requirement applies to userId (they have no
// 2FA and requirements are checked).
func (r groupRoles) blocked(userId string) (bool, error) {
	if r.twoFactor == nil {
		return false, nil
	}
	on, err := r.twoFactor(userId)
	return !on, err
}

// role is userId's role in group (none: no membership, direct or
// inherited) and the group that gives it.
func (r groupRoles) role(group *domain.Group, userId string) (domain.GroupRole, *domain.Group, error) {
	if !domain.IsPerson(userId) {
		return "", nil, nil
	}
	chain := []domain.Group{*group}
	if group.ParentId != nil {
		ancestors, err := r.groups.GetByNames(domain.GroupPathPrefixes(group.Name))
		if err != nil {
			return "", nil, err
		}
		chain = ancestors
	}
	ids := make([]string, len(chain))
	byId := map[string]*domain.Group{}
	for i := range chain {
		ids[i] = chain[i].Id
		byId[chain[i].Id] = &chain[i]
	}
	members, err := r.groups.MembershipsIn(userId, ids)
	if err != nil || len(members) == 0 {
		return "", nil, err
	}
	flags := map[string]bool{}
	for _, g := range chain {
		flags[g.Name] = g.RequireTwoFactor
	}
	if requires(group.Name, flags) {
		if blocked, err := r.blocked(userId); err != nil || blocked {
			return "", nil, err
		}
	}
	var best domain.GroupRole
	var from *domain.Group
	for _, m := range members {
		if m.Role.Level() > best.Level() || (m.Role.Level() == best.Level() && m.GroupId == group.Id) {
			best, from = m.Role, byId[m.GroupId]
		}
	}
	return best, from, nil
}

// effective maps every group userId has a role in (their memberships'
// groups and all groups below them) to that role.
func (r groupRoles) effective(userId string) (map[string]domain.GroupRole, error) {
	out := map[string]domain.GroupRole{}
	if !domain.IsPerson(userId) {
		return out, nil
	}
	members, err := r.groups.MembershipsOf(userId)
	if err != nil || len(members) == 0 {
		return out, err
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.GroupId)
	}
	blocked, err := r.blocked(userId)
	if err != nil {
		return nil, err
	}
	groups, err := r.groups.GetByIds(ids)
	if err != nil {
		return nil, err
	}
	byId := map[string]domain.Group{}
	for _, g := range groups {
		byId[g.Id] = g
	}
	flags := map[string]bool{} // require flags by path, for the groups seen
	raise := func(g domain.Group, role domain.GroupRole) {
		if blocked && requires(g.Name, flags) {
			return
		}
		if role.Level() > out[g.Id].Level() {
			out[g.Id] = role
		}
	}
	for _, m := range members {
		g, ok := byId[m.GroupId]
		if !ok {
			continue
		}
		below, err := r.groups.Descendants(g.Name)
		if err != nil {
			return nil, err
		}
		if blocked {
			ancestors, err := r.groups.GetByNames(domain.GroupPathPrefixes(g.Name))
			if err != nil {
				return nil, err
			}
			for _, a := range append(ancestors, below...) {
				flags[a.Name] = a.RequireTwoFactor
			}
		}
		raise(g, m.Role)
		for _, d := range below {
			raise(d, m.Role)
		}
	}
	return out, nil
}

// groupRoles for repository access (memberships filtered by the groups'
// requirements).
func (c *RepositoryApp) groupRoles() groupRoles {
	twoFactor := c.TwoFactorEnabled
	if twoFactor == nil {
		twoFactor = func(string) (bool, error) { return true, nil } // unknown: no requirement applies
	}
	return groupRoles{groups: c.GroupPers, twoFactor: twoFactor}
}

// effectiveGroupIds lists the groups userId's repository access comes
// through: their memberships' groups and those below them.
func (c *RepositoryApp) effectiveGroupIds(userId string) ([]string, error) {
	roles, err := c.groupRoles().effective(userId)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	return ids, nil
}

// groupRole is userId's role in the group for repository access.
func (c *RepositoryApp) groupRole(groupId, userId string) (domain.GroupRole, error) {
	g, err := c.GroupPers.GetById(groupId)
	if err != nil {
		return "", err
	}
	role, _, err := c.groupRoles().role(g, userId)
	return role, err
}
