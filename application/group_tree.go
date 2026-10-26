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
//   - for repository access, a membership counts only if its group allows
//     it (two-factor requirement, usableGroups);
//   - a sub-group's path must not be a repository's path (org/team the
//     group vs org/team the repository), and vice versa.

// groupRoles answers users' roles in groups, inherited down the tree.
type groupRoles struct {
	groups domain.GroupPers
	// usable filters memberships by the groups' requirements (nil: every
	// membership counts, for managing groups).
	usable func(userId string, groupIds []string) ([]string, error)
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
	if err != nil {
		return "", nil, err
	}
	usable := map[string]bool{}
	if r.usable != nil {
		var memberIds []string
		for _, m := range members {
			memberIds = append(memberIds, m.GroupId)
		}
		ok, err := r.usable(userId, memberIds)
		if err != nil {
			return "", nil, err
		}
		for _, id := range ok {
			usable[id] = true
		}
	}
	var best domain.GroupRole
	var from *domain.Group
	for _, m := range members {
		if r.usable != nil && !usable[m.GroupId] {
			continue
		}
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
	if r.usable != nil {
		if ids, err = r.usable(userId, ids); err != nil {
			return nil, err
		}
	}
	counted := map[string]bool{}
	for _, id := range ids {
		counted[id] = true
	}
	groups, err := r.groups.GetByIds(ids)
	if err != nil {
		return nil, err
	}
	byId := map[string]domain.Group{}
	for _, g := range groups {
		byId[g.Id] = g
	}
	raise := func(id string, role domain.GroupRole) {
		if role.Level() > out[id].Level() {
			out[id] = role
		}
	}
	for _, m := range members {
		g, ok := byId[m.GroupId]
		if !counted[m.GroupId] || !ok {
			continue
		}
		raise(g.Id, m.Role)
		below, err := r.groups.Descendants(g.Name)
		if err != nil {
			return nil, err
		}
		for _, d := range below {
			raise(d.Id, m.Role)
		}
	}
	return out, nil
}

// groupRoles for repository access (memberships filtered by the groups'
// requirements).
func (c *RepositoryApp) groupRoles() groupRoles {
	return groupRoles{groups: c.GroupPers, usable: c.usableGroups}
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
