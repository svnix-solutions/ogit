package application

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Group rules:
//   - any user can create a group and becomes its owner;
//   - only members can see a group or its members (others get not found);
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

// MembershipsFor returns userId's groups with their role in each.
func (c *GroupApp) MembershipsFor(userId string) ([]Membership, error) {
	groups, err := c.GroupPers.ListForUser(userId)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, 0, len(groups))
	for _, g := range groups {
		m, err := c.GroupPers.GetMember(g.Id, userId)
		if isNotFound(err) {
			continue // left the group meanwhile
		} else if err != nil {
			return nil, err
		}
		out = append(out, Membership{Group: g, Role: m.Role})
	}
	return out, nil
}

// Get returns the group identified by id or name, with the actor's
// membership. Groups the actor is not a member of are reported as not found.
func (c *GroupApp) Get(identifier, actorId string) (*domain.Group, *domain.GroupMember, error) {
	var group *domain.Group
	var err error
	if validator.IsValidUUID(identifier) {
		group, err = c.GroupPers.GetById(identifier)
	} else {
		group, err = c.GroupPers.GetByName(identifier)
	}
	if isNotFound(err) {
		return nil, nil, ErrGroupNotFound
	} else if err != nil {
		return nil, nil, err
	}

	actor, err := c.GroupPers.GetMember(group.Id, actorId)
	if isNotFound(err) {
		return nil, nil, ErrGroupNotFound
	} else if err != nil {
		return nil, nil, err
	}
	return group, actor, nil
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
	return c.GroupPers.Delete(group.Id)
}

func (c *GroupApp) ListMembers(identifier, actorId string) ([]domain.GroupMember, error) {
	group, _, err := c.Get(identifier, actorId)
	if err != nil {
		return nil, err
	}
	return c.GroupPers.ListMembers(group.Id)
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

	if err := c.GroupPers.TransferOwnership(group.Id, actorId, target.UserId); err != nil {
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
