package application

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/gosimple/slug"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type RepositoryApp struct {
	Config               config.Config
	Logger               zerolog.Logger
	RepoPres             domain.RepositoryPers
	RepositoryMemberPers domain.RepositoryMemberPers
	GroupPers            domain.GroupPers
	UserPers             domain.UserPers
	SSHKeyPres           domain.SSHKeyPers
	StorageConfig        storage.Config
	// Webhooks receives repository events; nil: none.
	Webhooks *WebhookApp
}

func NewRepositoryApp(config config.Config, logger zerolog.Logger, repoPers domain.RepositoryPers, repositoryMemberPers domain.RepositoryMemberPers, groupPers domain.GroupPers, userPers domain.UserPers, sshKeyPers domain.SSHKeyPers, storageConfig storage.Config) *RepositoryApp {
	return &RepositoryApp{
		Config:               config,
		Logger:               logger,
		RepoPres:             repoPers,
		GroupPers:            groupPers,
		UserPers:             userPers,
		SSHKeyPres:           sshKeyPers,
		StorageConfig:        storageConfig,
		RepositoryMemberPers: repositoryMemberPers,
	}
}

// Create makes a repository in namespace (the creator's own when empty).
// See namespace.go for who may create where.
func (c *RepositoryApp) Create(namespace, name, description, creator string, isPrivate bool) (*domain.Repository, error) {
	return c.create(namespace, creator, &domain.Repository{Name: name, Description: description, IsPrivate: isPrivate, DefaultBranch: "main", HasIssues: true},
		func(repo *domain.Repository) error {
			return c.StorageConfig.CreateRepository(repo.StorageKey, repo.Name)
		})
}

// create stores a repository made from tmpl (name, description, privacy,
// default branch, issues, fork) in namespace, then fills its storage with
// initStorage; on failure it removes what it made.
func (c *RepositoryApp) create(namespace, creator string, tmpl *domain.Repository, initStorage func(*domain.Repository) error) (*domain.Repository, error) {
	logger := c.Logger.With().Str("component", "application.repository.create").Logger()
	name, description, isPrivate := tmpl.Name, tmpl.Description, tmpl.IsPrivate

	if namespace == "" {
		user, err := c.UserPers.GetById(creator)
		if err != nil {
			return nil, err
		}
		namespace = user.Username
	}
	ns, err := c.namespaceForCreate(namespace, creator)
	if err != nil {
		return nil, err
	}

	name = strings.TrimSpace(name)
	repoSlug := slug.Make(name)
	if repoSlug == "" {
		return nil, ErrInvalidName
	}

	id := utils.UUIDv4()
	repo := &domain.Repository{
		Id:            id,
		Name:          name,
		Description:   description,
		IsPrivate:     isPrivate,
		Namespace:     ns.Path,
		OwnerType:     ns.Type,
		OwnerId:       ns.Id,
		Slug:          repoSlug,
		StorageKey:    id, // never changes, so renames do not move data
		DefaultBranch: tmpl.DefaultBranch,
		HasIssues:     tmpl.HasIssues,
		ForkOfId:      tmpl.ForkOfId,
	}

	err = c.RepoPres.Create(repo)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrRepositoryNameTaken
	} else if err != nil {
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("repo_name", repo.Name).Msg("failed to create repository")
		return nil, err
	}

	// The path now belongs to this repository, even if it used to redirect
	// to another one.
	if err := c.RepoPres.DropRedirect(repo.Namespace, repo.Slug); err != nil {
		logger.Warn().Err(err).Str("path", repo.FullPath()).Msg("failed to drop redirect")
	}

	err = initStorage(repo)
	if err != nil {
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("repo_name", repo.Name).Msg("failed to create repository storage")
		// Attempt to clean up the database entry if storage creation fails
		c.RepoPres.Delete(*repo)
		_ = c.StorageConfig.DeleteRepository(repo.StorageKey)
		return nil, fmt.Errorf("failed to create repository storage: %w", err)
	}

	// In a group's namespace the group's role applies (namespaceRole); the
	// creator gets no extra row.
	if ns.Type == domain.OwnerGroup {
		return repo, nil
	}
	member := &domain.RepositoryMember{
		Id:           utils.UUIDv4(),
		RepositoryId: repo.Id,
		MemberType:   domain.MemberTypeUser,
		UserId:       &creator,
		AccessType:   domain.AccessTypeOwner,
	}

	err = c.RepositoryMemberPers.Create(member)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		logger.Warn().Str("repo_id", repo.Id).Str("user_id", creator).Msg("repository member already exists")
		return nil, fmt.Errorf("repository member already exists")
	} else if err != nil {
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("user_id", creator).Msg("failed to create repository member")
		c.RepoPres.Delete(*repo)
		c.StorageConfig.DeleteRepository(repo.StorageKey)
		return nil, err
	}

	return repo, nil
}

// ErrRepositoryNotFound is returned when a repository does not exist or,
// from GetRepositoryForUser, when the user may not see it.
var ErrRepositoryNotFound = errors.New("repository not found")

// GetRepositoryForUser returns the repository identified by id or path
// (<namespace>/<slug>) if userId may read it. Private repositories the user
// cannot read are reported as ErrRepositoryNotFound so that their existence
// is not disclosed.
func (c *RepositoryApp) GetRepositoryForUser(id, path *string, userId string) (*domain.Repository, error) {
	repo, err := c.GetRepository(id, path)
	if err != nil {
		return nil, err
	}

	canRead, err := c.CanUserAccess(repo.Id, userId, "read")
	if err != nil {
		return nil, err
	}
	if !canRead {
		return nil, ErrRepositoryNotFound
	}
	return repo, nil
}

// GetRepository finds a repository by id or by path, <namespace>/<slug>
// (former paths of renamed repositories included). A flat path, as used
// before namespaces, is not found.
func (c *RepositoryApp) GetRepository(id, path *string) (*domain.Repository, error) {
	logger := c.Logger.With().Str("component", "application.repository.get").Logger()

	var repository *domain.Repository
	var err error
	switch {
	case id != nil:
		repository, err = c.RepoPres.GetRepository(domain.Repository{Id: *id})
	case path != nil:
		namespace, slug, ok := SplitPath(*path)
		if !ok {
			return nil, ErrRepositoryNotFound
		}
		repository, err = c.RepoPres.GetByPath(namespace, slug)
		if isNotFound(err) {
			if renamed, rerr := c.RepoPres.ResolveRedirect(namespace, slug); rerr == nil {
				return renamed, nil
			}
		}
	default:
		return nil, fmt.Errorf("either id or path must be provided")
	}
	switch helperError.Catch(err) {
	case nil:
	case gorm.ErrRecordNotFound:
		return nil, ErrRepositoryNotFound
	default:
		logger.Error().Err(err).Msg("failed to get repository")
		return nil, err
	}
	return repository, nil
}

// GetUserEffectiveRole returns the highest role userId has on the repository:
// directly, through one of their groups being a member, or through its
// namespace (their own, or a group's they belong to).
func (c *RepositoryApp) GetUserEffectiveRole(repositoryId, userId string) (domain.AccessType, error) {
	logger := c.Logger.With().Str("component", "application.repository.get_user_effective_role").Logger()

	// Anonymous callers have no role. (Postgres also rejects '' as a UUID,
	// so the queries below would fail instead of finding nothing.)
	if userId == "" {
		return domain.AccessTypeNone, nil
	}
	// A repository token reads its own repository, nothing else.
	if tokenRepo, ok := domain.RepoTokenRepository(userId); ok {
		if tokenRepo == repositoryId {
			return domain.AccessTypeReporter, nil
		}
		return domain.AccessTypeNone, nil
	}

	repo, err := c.GetRepository(&repositoryId, nil)
	if err != nil {
		return domain.AccessTypeNone, err
	}
	fromNamespace, err := c.namespaceRole(repo, userId)
	if err != nil {
		return domain.AccessTypeNone, err
	}

	members, err := c.RepositoryMemberPers.GetByRepositoryWithRelations(repositoryId)
	if err != nil {
		logger.Error().Err(err).Str("repository_id", repositoryId).Msg("failed to get repository members")
		return domain.AccessTypeNone, err
	}

	groupIds, err := c.GroupPers.GroupIdsForUser(userId)
	if err != nil {
		logger.Error().Err(err).Str("user_id", userId).Msg("failed to get user groups")
		return domain.AccessTypeNone, err
	}
	inGroup := make(map[string]bool, len(groupIds))
	for _, id := range groupIds {
		inGroup[id] = true
	}

	effectiveRole := fromNamespace
	for _, member := range members {
		switch member.MemberType {
		case domain.MemberTypeUser:
			if member.UserId != nil && *member.UserId == userId {
				effectiveRole = domain.MaxAccessType(effectiveRole, member.AccessType)
			}
		case domain.MemberTypeGroup:
			if member.GroupId != nil && inGroup[*member.GroupId] {
				effectiveRole = domain.MaxAccessType(effectiveRole, member.AccessType)
			}
		}
	}

	return effectiveRole, nil
}

func (c *RepositoryApp) CanUserAccess(repositoryId, userId, operation string) (bool, error) {
	// Récupérer le repository pour vérifier s'il est privé
	repo, err := c.GetRepository(&repositoryId, nil)
	if err != nil {
		return false, err
	}

	// Si le repo est public et l'opération est de lecture, autoriser
	if !repo.IsPrivate && operation == "read" {
		return true, nil
	}

	// Sinon, vérifier les permissions
	effectiveRole, err := c.GetUserEffectiveRole(repositoryId, userId)
	if err != nil {
		return false, err
	}

	switch operation {
	case "read":
		return effectiveRole.CanRead(), nil
	case "write":
		return effectiveRole.CanWrite(), nil
	case "admin":
		return effectiveRole.CanAdmin(), nil
	default:
		return false, fmt.Errorf("unknown operation: %s", operation)
	}
}
