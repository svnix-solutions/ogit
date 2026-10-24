package application

import (
	"errors"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Repository membership rules:
//   - anyone who can read the repository can list its members;
//   - owners can add, change and remove any member;
//   - maintainers can do so only for roles up to developer, on members
//     whose current role is at most developer;
//   - groups can be given at most maintainer, and only by someone who is a
//     member of the group;
//   - anyone can remove their own (direct) membership;
//   - a repository always keeps at least one owner (domain.ErrLastOwner).

// AddMemberInput identifies who to add: exactly one of Username or Group.
type AddMemberInput struct {
	Username string
	Group    string
	Role     domain.AccessType
}

// ListMembers returns the repository's members if actorId can read it.
func (c *RepositoryApp) ListMembers(id, slug *string, actorId string) ([]domain.RepositoryMember, error) {
	repo, err := c.GetRepositoryForUser(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	return c.RepositoryMemberPers.GetByRepositoryWithRelations(repo.Id)
}

// ListForUser returns the repositories userId is a member of, directly or
// through a group.
func (c *RepositoryApp) ListForUser(userId string) ([]domain.Repository, error) {
	groupIds, err := c.GroupPers.GroupIdsForUser(userId)
	if err != nil {
		return nil, err
	}
	return c.RepositoryMemberPers.ListRepositoriesForMember(userId, groupIds)
}

func (c *RepositoryApp) AddMember(id, slug *string, actorId string, in AddMemberInput) (*domain.RepositoryMember, error) {
	if (in.Username == "") == (in.Group == "") {
		return nil, ErrInvalidMember
	}
	if !in.Role.IsAssignable() {
		return nil, ErrInvalidRole
	}

	repo, actorRole, err := c.repositoryForManager(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	if err := checkCanManage(actorRole, nil, in.Role); err != nil {
		return nil, err
	}

	member := &domain.RepositoryMember{
		Id:           utils.UUIDv4(),
		RepositoryId: repo.Id,
		AccessType:   in.Role,
	}

	if in.Username != "" {
		user, err := c.UserPers.GetByUsername(in.Username)
		if isNotFound(err) {
			return nil, ErrUserNotFound
		} else if err != nil {
			return nil, err
		}
		if err := absent(c.RepositoryMemberPers.GetUserMember(repo.Id, user.Id)); err != nil {
			return nil, err
		}
		member.MemberType = domain.MemberTypeUser
		member.UserId = &user.Id
		member.User = user
	} else {
		if in.Role.Level() > domain.AccessTypeMaintainer.Level() {
			return nil, ErrInvalidRole // groups can be at most maintainer
		}
		group, err := c.GroupPers.GetByName(in.Group)
		if isNotFound(err) {
			return nil, ErrGroupNotFound
		} else if err != nil {
			return nil, err
		}
		// Only groups the actor belongs to; others are not visible to them.
		if _, err := c.GroupPers.GetMember(group.Id, actorId); isNotFound(err) {
			return nil, ErrGroupNotFound
		} else if err != nil {
			return nil, err
		}
		if err := absent(c.RepositoryMemberPers.GetGroupMember(repo.Id, group.Id)); err != nil {
			return nil, err
		}
		member.MemberType = domain.MemberTypeGroup
		member.GroupId = &group.Id
		member.Group = *group
	}

	createErr := c.RepositoryMemberPers.Create(member)
	if helperError.Catch(createErr) == gorm.ErrDuplicatedKey {
		return nil, ErrAlreadyMember
	} else if createErr != nil {
		return nil, createErr
	}
	return member, nil
}

func (c *RepositoryApp) UpdateMember(id, slug *string, actorId, memberId string, role domain.AccessType) (*domain.RepositoryMember, error) {
	if !role.IsAssignable() {
		return nil, ErrInvalidRole
	}

	repo, actorRole, err := c.repositoryForManager(id, slug, actorId)
	if err != nil {
		return nil, err
	}
	member, err := c.RepositoryMemberPers.GetById(repo.Id, memberId)
	if isNotFound(err) {
		return nil, ErrMemberNotFound
	} else if err != nil {
		return nil, err
	}
	if member.MemberType == domain.MemberTypeGroup && role.Level() > domain.AccessTypeMaintainer.Level() {
		return nil, ErrInvalidRole
	}
	if err := checkCanManage(actorRole, &member.AccessType, role); err != nil {
		return nil, err
	}

	if err := c.RepositoryMemberPers.UpdateAccess(member, role); err != nil {
		return nil, err
	}
	member.AccessType = role
	return member, nil
}

func (c *RepositoryApp) RemoveMember(id, slug *string, actorId, memberId string) error {
	repo, err := c.GetRepositoryForUser(id, slug, actorId)
	if err != nil {
		return err
	}
	member, err := c.RepositoryMemberPers.GetById(repo.Id, memberId)
	if isNotFound(err) {
		return ErrMemberNotFound
	} else if err != nil {
		return err
	}

	leaving := member.MemberType == domain.MemberTypeUser && member.UserId != nil && *member.UserId == actorId
	if !leaving {
		actorRole, err := c.GetUserEffectiveRole(repo.Id, actorId)
		if err != nil {
			return err
		}
		if err := checkCanManage(actorRole, &member.AccessType, domain.AccessTypeNone); err != nil {
			return err
		}
	}

	return c.RepositoryMemberPers.Delete(member)
}

// repositoryForManager returns the repository if actorId can read it, with
// actorId's effective role on it.
func (c *RepositoryApp) repositoryForManager(id, slug *string, actorId string) (*domain.Repository, domain.AccessType, error) {
	repo, err := c.GetRepositoryForUser(id, slug, actorId)
	if err != nil {
		return nil, domain.AccessTypeNone, err
	}
	role, err := c.GetUserEffectiveRole(repo.Id, actorId)
	if err != nil {
		return nil, domain.AccessTypeNone, err
	}
	return repo, role, nil
}

// checkCanManage enforces the owner/maintainer rules for changing a member
// from current (nil when adding) to next (AccessTypeNone when removing).
func checkCanManage(actor domain.AccessType, current *domain.AccessType, next domain.AccessType) error {
	if actor == domain.AccessTypeOwner {
		return nil
	}
	if actor != domain.AccessTypeMaintainer {
		return ErrForbidden
	}
	limit := domain.AccessTypeDeveloper.Level()
	if next.Level() > limit || (current != nil && current.Level() > limit) {
		return ErrForbidden
	}
	return nil
}

// absent returns nil if a membership lookup found nothing, ErrAlreadyMember
// if it found one, and the lookup error otherwise.
func absent(_ *domain.RepositoryMember, err error) error {
	switch {
	case err == nil:
		return ErrAlreadyMember
	case isNotFound(err):
		return nil
	default:
		return err
	}
}

// isNotFound reports whether err means a row was not found.
func isNotFound(err error) bool {
	return errors.Is(helperError.Catch(err), gorm.ErrRecordNotFound)
}
