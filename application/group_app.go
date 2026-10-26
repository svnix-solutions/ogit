package application

import (
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Group rules (sub-groups: group_tree.go):
//   - any user can create a group and becomes its owner; maintainers and
//     owners of a group create sub-groups in it and own them;
//   - only members (direct or inherited) can see a group or its members
//     (others get not found);
//   - roles are inherited down the tree: a sub-group's member list shows
//     inherited members, and a direct membership can only raise a role;
//   - a group with sub-groups can't be deleted;
//   - the owner and maintainers add and remove members;
//   - only the owner promotes or demotes maintainers, removes maintainers,
//     transfers ownership or deletes the group;
//   - anyone but the owner can leave; the owner must transfer first.

type GroupApp struct {
	Config    config.Config
	Logger    zerolog.Logger
	GroupPers domain.GroupPers
	UserPers  domain.UserPers
	RepoPers  domain.RepositoryPers
	// TwoFactor tells owners and maintainers which members lack 2FA; nil:
	// unknown.
	TwoFactor *TwoFactorApp
	// Packages: a group with packages can't be deleted; nil: none to check.
	Packages domain.PackagesPers
}

func NewGroupApp(config config.Config, logger zerolog.Logger, groupPers domain.GroupPers, userPers domain.UserPers, repoPers domain.RepositoryPers) *GroupApp {
	return &GroupApp{Config: config, Logger: logger, GroupPers: groupPers, UserPers: userPers, RepoPers: repoPers}
}

// Create makes a group; its name is also its namespace (see namespace.go).
func (c *GroupApp) Create(actorId, name, description string) (*domain.Group, error) {
	switch err := checkNewNamespace(c.UserPers, c.GroupPers, name); err {
	case nil:
	case domain.ErrInvalidNamespaceName:
		return nil, ErrInvalidGroupName
	case ErrNamespaceTaken:
		return nil, ErrGroupNameTaken
	default:
		return nil, err
	}

	group := &domain.Group{
		Id:          utils.UUIDv4(),
		Name:        name,
		Description: description,
		Role:        domain.RoleUser,
		OwnerId:     actorId,
	}
	owner := &domain.GroupMember{
		Id:      utils.UUIDv4(),
		GroupId: group.Id,
		UserId:  actorId,
		Role:    domain.GroupRoleOwner,
	}

	err := c.GroupPers.Create(group, owner)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrGroupNameTaken
	} else if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.group.create").Str("name", name).Msg("failed to create group")
		return nil, err
	}
	return group, nil
}

func (c *GroupApp) ListForUser(userId string) ([]domain.Group, error) {
	return c.GroupPers.ListForUser(userId)
}

// Membership is a group with the user's role in it.
type Membership struct {
	domain.Group
	Role domain.GroupRole
}

// MembershipsFor returns userId's groups (sub-groups of theirs included)
// with their role in each, by path.
func (c *GroupApp) MembershipsFor(userId string) ([]Membership, error) {
	roles, err := c.roles().effective(userId)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(roles))
	for id := range roles {
		ids = append(ids, id)
	}
	groups, err := c.GroupPers.GetByIds(ids)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(groups))
	for _, g := range groups {
		out = append(out, Membership{Group: g, Role: roles[g.Id]})
	}
	return out, nil
}

// roles: group roles for managing groups (2FA requirements don't apply).
func (c *GroupApp) roles() groupRoles { return groupRoles{groups: c.GroupPers} }

// Get returns the group identified by id or name, with the actor's
// membership. Groups the actor is not a member of are reported as not found.
func (c *GroupApp) Get(identifier, actorId string) (*domain.Group, *domain.GroupMember, error) {
	group, err := c.lookup(identifier)
	if err != nil {
		return nil, nil, err
	}
	role, _, err := c.roles().role(group, actorId)
	if err != nil {
		return nil, nil, err
	}
	if role == "" {
		return nil, nil, ErrGroupNotFound
	}
	// The actor's effective role (it may come from a parent group).
	return group, &domain.GroupMember{GroupId: group.Id, UserId: actorId, Role: role}, nil
}

// lookup finds a group by id or path (its slashes may come URL-encoded).
func (c *GroupApp) lookup(identifier string) (*domain.Group, error) {
	var group *domain.Group
	var err error
	if validator.IsValidUUID(identifier) {
		group, err = c.GroupPers.GetById(identifier)
	} else {
		if decoded, uerr := url.PathUnescape(identifier); uerr == nil {
			identifier = decoded
		}
		group, err = c.GroupPers.GetByName(identifier)
	}
	if isNotFound(err) {
		return nil, ErrGroupNotFound
	}
	return group, err
}

// CreateSubgroup makes a sub-group of parent (its maintainers and owners
// may); the actor owns it.
func (c *GroupApp) CreateSubgroup(actorId, parent, name, description string) (*domain.Group, error) {
	p, actor, err := c.Get(parent, actorId)
	if err != nil {
		return nil, err
	}
	if actor.Role.Level() < domain.GroupRoleMaintainer.Level() {
		return nil, ErrForbidden
	}
	path := p.Name + "/" + name
	if strings.Contains(name, "/") || domain.ValidGroupPath(path) != nil {
		return nil, ErrInvalidGroupName
	}
	// org/team the group and org/team the repository would share a URL.
	if _, err := c.RepoPers.GetByPath(p.Name, name); err == nil {
		return nil, ErrGroupNameTaken
	} else if !isNotFound(err) {
		return nil, err
	}
	group := &domain.Group{Id: utils.UUIDv4(), Name: path, Description: description, ParentId: &p.Id, Role: domain.RoleUser, OwnerId: actorId}
	owner := &domain.GroupMember{Id: utils.UUIDv4(), GroupId: group.Id, UserId: actorId, Role: domain.GroupRoleOwner}
	err = c.GroupPers.Create(group, owner)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrGroupNameTaken
	} else if err != nil {
		return nil, err
	}
	c.Logger.Info().Str("component", "application.group").Str("group", path).Str("actor_id", actorId).Msg("sub-group created")
	return group, nil
}

// Subgroups lists a group's sub-groups (members see them all).
func (c *GroupApp) Subgroups(identifier, actorId string) ([]domain.Group, error) {
	group, _, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	return c.GroupPers.Children(group.Id)
}

// inheritedRole is userId's role from group's ancestors (none at the top).
func (c *GroupApp) inheritedRole(group *domain.Group, userId string) (domain.GroupRole, error) {
	if group.ParentId == nil {
		return "", nil
	}
	parent, err := c.GroupPers.GetById(*group.ParentId)
	if err != nil {
		return "", err
	}
	role, _, err := c.roles().role(parent, userId)
	return role, err
}

func (c *GroupApp) Delete(identifier, actorId string) error {
	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return err
	}
	if actor.Role != domain.GroupRoleOwner {
		return ErrForbidden
	}
	if repos, err := c.RepoPers.InNamespace(domain.OwnerGroup, group.Id); err != nil {
		return err
	} else if len(repos) > 0 {
		return ErrNamespaceNotEmpty
	}
	if children, err := c.GroupPers.Children(group.Id); err != nil {
		return err
	} else if len(children) > 0 {
		return ErrGroupHasSubgroups
	}
	if c.Packages != nil {
		if packages, err := c.Packages.ListPackages(group.Id, ""); err != nil {
			return err
		} else if len(packages) > 0 {
			return ErrNamespaceHasPackages
		}
	}
	return c.GroupPers.Delete(group.Id)
}

// GroupMemberView is a member of a group, direct or inherited from an
// ancestor (InheritedFrom is then its path).
type GroupMemberView struct {
	domain.GroupMember
	InheritedFrom string
}

// ListMembers lists a group's members: its own, then those inherited from
// its ancestors (with their highest role, unless a direct membership
// raises it).
func (c *GroupApp) ListMembers(identifier, actorId string) ([]GroupMemberView, error) {
	group, _, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	chain := []domain.Group{*group}
	if group.ParentId != nil {
		if chain, err = c.GroupPers.GetByNames(domain.GroupPathPrefixes(group.Name)); err != nil {
			return nil, err
		}
	}
	best := map[string]GroupMemberView{}
	var order []string
	// Nearest group last, so a direct membership wins a tie.
	for _, g := range chain {
		members, err := c.GroupPers.ListMembers(g.Id)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			v := GroupMemberView{GroupMember: m}
			if g.Id != group.Id {
				v.InheritedFrom = g.Name
			}
			old, seen := best[m.UserId]
			if !seen {
				order = append(order, m.UserId)
			}
			if !seen || m.Role.Level() >= old.Role.Level() {
				best[m.UserId] = v
			}
		}
	}
	out := make([]GroupMemberView, 0, len(order))
	for _, id := range order {
		if best[id].InheritedFrom == "" {
			out = append(out, best[id])
		}
	}
	for _, id := range order {
		if best[id].InheritedFrom != "" {
			out = append(out, best[id])
		}
	}
	return out, nil
}

// MembersTwoFactor tells owners and maintainers which members have 2FA
// (nil for other members: it is none of their business).
func (c *GroupApp) MembersTwoFactor(identifier, actorId string, members []domain.GroupMember) (map[string]bool, error) {
	_, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	if c.TwoFactor == nil || (actor.Role != domain.GroupRoleOwner && actor.Role != domain.GroupRoleMaintainer) {
		return nil, nil
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.UserId)
	}
	return c.TwoFactor.EnabledUsers(ids)
}

// SetRequireTwoFactor is for the group's owner: members without 2FA then
// get no access to the group's repositories through it.
func (c *GroupApp) SetRequireTwoFactor(identifier, actorId string, on bool) (*domain.Group, error) {
	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.GroupRoleOwner {
		return nil, ErrForbidden
	}
	if err := c.GroupPers.SetRequireTwoFactor(group.Id, on); err != nil {
		return nil, err
	}
	c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", "group.require_2fa").Str("group", group.Name).Bool("on", on).
		Msg("group two-factor requirement changed")
	group.RequireTwoFactor = on
	return group, nil
}

func (c *GroupApp) AddMember(identifier, actorId, username string, role domain.GroupRole) (*domain.GroupMember, error) {
	if role != domain.GroupRoleMember && role != domain.GroupRoleMaintainer {
		return nil, ErrInvalidRole // ownership changes go through TransferOwnership
	}

	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	if actor.Role.Level() < domain.GroupRoleMaintainer.Level() ||
		(role == domain.GroupRoleMaintainer && actor.Role != domain.GroupRoleOwner) {
		return nil, ErrForbidden
	}

	user, err := c.UserPers.GetByUsername(username)
	if isNotFound(err) {
		return nil, ErrUserNotFound
	} else if err != nil {
		return nil, err
	}
	if inherited, err := c.inheritedRole(group, user.Id); err != nil {
		return nil, err
	} else if role.Level() <= inherited.Level() {
		return nil, ErrInheritedRole
	}

	member := &domain.GroupMember{
		Id:      utils.UUIDv4(),
		GroupId: group.Id,
		UserId:  user.Id,
		User:    user,
		Role:    role,
	}
	err = c.GroupPers.AddMember(member)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrAlreadyMember
	} else if err != nil {
		return nil, err
	}
	return member, nil
}

// UpdateMemberRole switches a member between member and maintainer; only the
// owner can.
func (c *GroupApp) UpdateMemberRole(identifier, actorId, username string, role domain.GroupRole) (*domain.GroupMember, error) {
	if role != domain.GroupRoleMember && role != domain.GroupRoleMaintainer {
		return nil, ErrInvalidRole
	}

	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.GroupRoleOwner {
		return nil, ErrForbidden
	}

	target, err := c.memberByUsername(group.Id, username)
	if err != nil {
		return nil, err
	}
	if target.Role == domain.GroupRoleOwner {
		return nil, ErrOwnerMustTransfer
	}
	if inherited, err := c.inheritedRole(group, target.UserId); err != nil {
		return nil, err
	} else if role.Level() <= inherited.Level() {
		return nil, ErrInheritedRole
	}

	if err := c.GroupPers.UpdateMemberRole(group.Id, target.UserId, role); err != nil {
		return nil, err
	}
	target.Role = role
	return target, nil
}

func (c *GroupApp) RemoveMember(identifier, actorId, username string) error {
	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return err
	}

	target, err := c.memberByUsername(group.Id, username)
	if err != nil {
		return err
	}
	if target.Role == domain.GroupRoleOwner {
		return ErrOwnerMustTransfer
	}

	leaving := target.UserId == actorId
	if !leaving {
		switch {
		case actor.Role == domain.GroupRoleOwner:
		case actor.Role == domain.GroupRoleMaintainer && target.Role == domain.GroupRoleMember:
		default:
			return ErrForbidden
		}
	}

	return c.GroupPers.RemoveMember(group.Id, target.UserId)
}

// TransferOwnership makes username (an existing member) the owner; the
// current owner becomes a maintainer.
func (c *GroupApp) TransferOwnership(identifier, actorId, username string) (*domain.Group, error) {
	group, actor, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	if actor.Role != domain.GroupRoleOwner {
		return nil, ErrForbidden
	}

	target, err := c.memberByUsername(group.Id, username)
	if err != nil {
		return nil, err
	}
	if target.UserId == actorId {
		return group, nil
	}

	// The current owner, not the actor: an owner of a parent group may
	// transfer a sub-group.
	if err := c.GroupPers.TransferOwnership(group.Id, group.OwnerId, target.UserId); err != nil {
		return nil, err
	}
	group.OwnerId = target.UserId
	return group, nil
}

func (c *GroupApp) memberByUsername(groupId, username string) (*domain.GroupMember, error) {
	user, err := c.UserPers.GetByUsername(username)
	if isNotFound(err) {
		return nil, ErrMemberNotFound
	} else if err != nil {
		return nil, err
	}
	member, err := c.GroupPers.GetMember(groupId, user.Id)
	if isNotFound(err) {
		return nil, ErrMemberNotFound
	} else if err != nil {
		return nil, err
	}
	return member, nil
}
