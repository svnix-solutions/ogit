package application

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// Site administration (/api/v1/admin). Rules:
//   - only active users with role admin; checked against the database on
//     every call;
//   - admins have no implicit access to repositories; to get it they make
//     someone (possibly themselves) an owner, which shows in the member list;
//   - the API never removes the last active admin (demote, disable, delete);
//     the `ogit admin` CLI is the recovery path;
//   - disabling a user ends their sessions immediately;
//   - a user is not deleted while they are the sole owner of a repository or
//     own a group (ErrWouldOrphan lists them);
//   - deleting a repository removes its git data and must be confirmed with
//     its slug.

var (
	ErrLastAdmin       = errors.New("the last active admin cannot be removed; promote another admin first")
	ErrWouldOrphan     = errors.New("user still owns repositories or groups; hand them over first")
	ErrConfirmMismatch = errors.New("confirm must repeat the repository slug")
	ErrAccountTaken    = errors.New("username or email already in use")
)

// OrphanError details ErrWouldOrphan.
type OrphanError struct {
	Repositories []string // paths of repositories the user solely owns or that are in their namespace
	Groups       []string // names of groups the user owns
}

func (e *OrphanError) Error() string {
	var parts []string
	if len(e.Repositories) > 0 {
		parts = append(parts, "repositories: "+strings.Join(e.Repositories, ", "))
	}
	if len(e.Groups) > 0 {
		parts = append(parts, "groups: "+strings.Join(e.Groups, ", "))
	}
	return ErrWouldOrphan.Error() + " (" + strings.Join(parts, "; ") + ")"
}

func (e *OrphanError) Is(target error) bool { return target == ErrWouldOrphan }

type AdminApp struct {
	Config               config.Config
	Logger               zerolog.Logger
	UserPers             domain.UserPers
	SessionPers          domain.SessionPers
	RepoPers             domain.RepositoryPers
	RepositoryMemberPers domain.RepositoryMemberPers
	GroupPers            domain.GroupPers
	Storage              storage.Config
}

// UserUpdate holds the changes for UpdateUser; nil fields are left alone.
type UserUpdate struct {
	Active        *bool
	EmailVerified *bool // only true is meaningful: mark verified
	Role          *domain.Role
}

// RequireAdmin returns ErrForbidden unless actorId is an active admin.
func (c *AdminApp) RequireAdmin(actorId string) error {
	u, err := c.UserPers.GetById(actorId)
	if err != nil || !u.Active || u.Role != domain.RoleAdmin {
		return ErrForbidden
	}
	return nil
}

func (c *AdminApp) ListUsers(actorId, query string, limit, offset int) ([]domain.User, int64, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, 0, err
	}
	return c.UserPers.List(query, clampLimit(limit), max(offset, 0))
}

// CreateUser creates a verified account with an initial password. The
// password policy applies; registration.enabled and the domain whitelist do
// not.
func (c *AdminApp) CreateUser(actorId, username, email, password string) (*domain.User, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	if err := checkPasswordPolicy(c.Config, password); err != nil {
		return nil, err
	}
	switch err := checkNewNamespace(c.UserPers, c.GroupPers, username); err {
	case nil:
	case ErrNamespaceTaken:
		return nil, ErrAccountTaken
	default:
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	user := &domain.User{
		Id:              utils.UUIDv4(),
		Username:        username,
		Email:           email,
		Password:        string(hash),
		Active:          true,
		EmailVerifiedAt: &now,
	}
	err = c.UserPers.Create(user)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrAccountTaken
	} else if err != nil {
		return nil, err
	}
	c.audit(actorId, "user.create").Str("user_id", user.Id).Send()
	return user, nil
}

func (c *AdminApp) UpdateUser(actorId, username string, upd UserUpdate) (*domain.User, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	user, err := c.userByUsername(username)
	if err != nil {
		return nil, err
	}

	removesAdmin := user.Role == domain.RoleAdmin && user.Active &&
		((upd.Active != nil && !*upd.Active) || (upd.Role != nil && *upd.Role != domain.RoleAdmin))
	if removesAdmin {
		if err := c.ensureAnotherAdmin(); err != nil {
			return nil, err
		}
	}

	if upd.Role != nil {
		if *upd.Role != domain.RoleAdmin && *upd.Role != domain.RoleUser {
			return nil, ErrInvalidRole
		}
		if err := c.UserPers.SetRole(user.Id, *upd.Role); err != nil {
			return nil, err
		}
		user.Role = *upd.Role
		c.audit(actorId, "user.role").Str("user_id", user.Id).Str("role", string(*upd.Role)).Send()
	}
	if upd.EmailVerified != nil && *upd.EmailVerified && user.EmailVerifiedAt == nil {
		now := time.Now()
		if err := c.UserPers.MarkEmailVerified(user.Id, now); err != nil {
			return nil, err
		}
		user.EmailVerifiedAt = &now
		c.audit(actorId, "user.verify").Str("user_id", user.Id).Send()
	}
	if upd.Active != nil && *upd.Active != user.Active {
		if err := c.UserPers.SetActive(user.Id, *upd.Active); err != nil {
			return nil, err
		}
		if !*upd.Active {
			// Disabling takes effect immediately.
			if err := c.SessionPers.DeleteByUserId(user.Id, ""); err != nil {
				return nil, err
			}
		}
		user.Active = *upd.Active
		c.audit(actorId, "user.active").Str("user_id", user.Id).Bool("active", *upd.Active).Send()
	}
	return user, nil
}

func (c *AdminApp) DeleteUser(actorId, username string) error {
	if err := c.RequireAdmin(actorId); err != nil {
		return err
	}
	user, err := c.userByUsername(username)
	if err != nil {
		return err
	}
	if user.Role == domain.RoleAdmin && user.Active {
		if err := c.ensureAnotherAdmin(); err != nil {
			return err
		}
	}

	repos, err := c.RepoPers.SoleOwnedBy(user.Id)
	if err != nil {
		return err
	}
	// Repositories in the user's namespace would lose their path even if
	// someone else also owns them.
	inNamespace, err := c.RepoPers.InNamespace(domain.OwnerUser, user.Id)
	if err != nil {
		return err
	}
	groups, err := c.GroupPers.OwnedBy(user.Id)
	if err != nil {
		return err
	}
	if len(repos) > 0 || len(inNamespace) > 0 || len(groups) > 0 {
		orphan := &OrphanError{}
		seen := map[string]bool{}
		for _, r := range append(inNamespace, repos...) {
			if !seen[r.Id] {
				seen[r.Id] = true
				orphan.Repositories = append(orphan.Repositories, r.FullPath())
			}
		}
		for _, g := range groups {
			orphan.Groups = append(orphan.Groups, g.Name)
		}
		return orphan
	}

	if err := c.UserPers.Purge(user.Id); err != nil {
		return err
	}
	c.audit(actorId, "user.delete").Str("user_id", user.Id).Str("username", user.Username).Send()
	return nil
}

func (c *AdminApp) ListRepositories(actorId string, limit, offset int) ([]domain.Repository, int64, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, 0, err
	}
	return c.RepoPers.List(clampLimit(limit), max(offset, 0))
}

// SetRepositoryOwner makes username an owner of the repository, adding them
// if they are not a member (recovery when owners are gone).
func (c *AdminApp) SetRepositoryOwner(actorId, identifier, username string) (*domain.RepositoryMember, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	repo, err := c.repository(identifier)
	if err != nil {
		return nil, err
	}
	user, err := c.userByUsername(username)
	if err != nil {
		return nil, err
	}

	member, err := c.RepositoryMemberPers.GetUserMember(repo.Id, user.Id)
	switch {
	case err == nil:
		if err := c.RepositoryMemberPers.UpdateAccess(member, domain.AccessTypeOwner); err != nil {
			return nil, err
		}
		member.AccessType = domain.AccessTypeOwner
	case isNotFound(err):
		member = &domain.RepositoryMember{
			Id:           utils.UUIDv4(),
			RepositoryId: repo.Id,
			MemberType:   domain.MemberTypeUser,
			UserId:       &user.Id,
			AccessType:   domain.AccessTypeOwner,
		}
		if err := c.RepositoryMemberPers.Create(member); err != nil {
			return nil, err
		}
	default:
		return nil, err
	}
	member.User = *user
	c.audit(actorId, "repository.owner").Str("repository_id", repo.Id).Str("user_id", user.Id).Send()
	return member, nil
}

// DeleteRepository removes the repository, its memberships and its git data.
func (c *AdminApp) DeleteRepository(actorId, identifier, confirm string) error {
	if err := c.RequireAdmin(actorId); err != nil {
		return err
	}
	repo, err := c.repository(identifier)
	if err != nil {
		return err
	}
	if confirm != repo.Slug {
		return ErrConfirmMismatch
	}

	if err := purgeRepository(c.RepoPers, c.Storage, c.Logger, repo); err != nil {
		return err
	}
	c.audit(actorId, "repository.delete").Str("repository_id", repo.Id).Str("path", repo.FullPath()).Send()
	return nil
}

// TransferGroup makes username the owner of the group, adding them first if
// needed (recovery when the owner is gone).
func (c *AdminApp) TransferGroup(actorId, identifier, username string) (*domain.Group, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	var group *domain.Group
	var err error
	if validator.IsValidUUID(identifier) {
		group, err = c.GroupPers.GetById(identifier)
	} else {
		group, err = c.GroupPers.GetByName(identifier)
	}
	if isNotFound(err) {
		return nil, ErrGroupNotFound
	} else if err != nil {
		return nil, err
	}
	user, err := c.userByUsername(username)
	if err != nil {
		return nil, err
	}
	if user.Id == group.OwnerId {
		return group, nil
	}

	if _, err := c.GroupPers.GetMember(group.Id, user.Id); isNotFound(err) {
		if err := c.GroupPers.AddMember(&domain.GroupMember{
			Id: utils.UUIDv4(), GroupId: group.Id, UserId: user.Id, Role: domain.GroupRoleMaintainer,
		}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if err := c.GroupPers.TransferOwnership(group.Id, group.OwnerId, user.Id); err != nil {
		return nil, err
	}
	group.OwnerId = user.Id
	c.audit(actorId, "group.transfer").Str("group_id", group.Id).Str("user_id", user.Id).Send()
	return group, nil
}

func (c *AdminApp) ensureAnotherAdmin() error {
	n, err := c.UserPers.CountActiveAdmins()
	if err != nil {
		return err
	}
	if n <= 1 {
		return ErrLastAdmin
	}
	return nil
}

func (c *AdminApp) userByUsername(username string) (*domain.User, error) {
	u, err := c.UserPers.GetByUsername(username)
	if isNotFound(err) {
		return nil, ErrUserNotFound
	} else if err != nil {
		return nil, err
	}
	return &u, nil
}

// TransferRepository moves a repository to another user's or group's
// namespace, without the permission checks of RepositoryApp.Transfer.
func (c *AdminApp) TransferRepository(actorId, identifier, namespace string) (*domain.Repository, error) {
	if err := c.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	repo, err := c.repository(identifier)
	if err != nil {
		return nil, err
	}
	ns, err := resolveNamespace(c.UserPers, c.GroupPers, namespace)
	if err != nil {
		return nil, err
	}
	from := repo.FullPath()
	if repo, err = moveRepository(c.RepoPers, repo, ns); err != nil {
		return nil, err
	}
	c.audit(actorId, "repository.transfer").Str("repository_id", repo.Id).Str("from", from).Str("to", repo.FullPath()).Send()
	return repo, nil
}

// repository finds a repository by id or current path (<namespace>/<slug>;
// redirects are not followed, so an admin acts on what they named).
func (c *AdminApp) repository(identifier string) (*domain.Repository, error) {
	var repo *domain.Repository
	var err error
	if validator.IsValidUUID(identifier) {
		repo, err = c.RepoPers.GetRepository(domain.Repository{Id: identifier})
	} else if namespace, slug, ok := SplitPath(identifier); ok {
		repo, err = c.RepoPers.GetByPath(namespace, slug)
	} else {
		return nil, ErrRepositoryNotFound
	}
	if isNotFound(err) {
		return nil, ErrRepositoryNotFound
	} else if err != nil {
		return nil, err
	}
	return repo, nil
}

// audit logs an admin action at info level, so it is kept with default
// logging.
func (c *AdminApp) audit(actorId, action string) *zerolog.Event {
	return c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", action)
}

func clampLimit(limit int) int {
	if limit <= 0 || limit > 200 {
		return 50
	}
	return limit
}
