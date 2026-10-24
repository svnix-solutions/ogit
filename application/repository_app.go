package application

import (
	"errors"
	"fmt"

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

func (c *RepositoryApp) Create(name, description, creator string, isPrivate bool) (*domain.Repository, error) {
	logger := c.Logger.With().Str("component", "application.repository.create").Logger()

	id := utils.UUIDv4()
	repo := &domain.Repository{
		Id:            id,
		Name:          name,
		Description:   description,
		IsPrivate:     isPrivate,
		Slug:          slug.Make(name),
		StorageKey:    id, // never changes, so renames do not move data
		DefaultBranch: "main",
	}

	err := c.RepoPres.Create(repo)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		logger.Warn().Str("repo_name", name).Msg("repository with the same name already exists")
		return nil, fmt.Errorf("repository with the same name already exists")
	} else if err != nil {
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("repo_name", repo.Name).Msg("failed to create repository")
		return nil, err
	}

	// The slug now belongs to this repository, even if it used to redirect
	// to another one.
	if err := c.RepoPres.DropRedirect(repo.Slug); err != nil {
		logger.Warn().Err(err).Str("slug", repo.Slug).Msg("failed to drop redirect")
	}

	err = c.StorageConfig.CreateRepository(repo.StorageKey)
	if err != nil {
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("repo_name", repo.Name).Msg("failed to create repository storage")
		// Attempt to clean up the database entry if storage creation fails
		c.RepoPres.Delete(*repo)
		return nil, fmt.Errorf("failed to create repository storage: %w", err)
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

// GetRepositoryForUser returns the repository identified by id or slug if
// userId may read it. Private repositories the user cannot read are reported
// as ErrRepositoryNotFound so that their existence is not disclosed.
func (c *RepositoryApp) GetRepositoryForUser(id, slug *string, userId string) (*domain.Repository, error) {
	repo, err := c.GetRepository(id, slug)
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

func (c *RepositoryApp) GetRepository(id, slug *string) (*domain.Repository, error) {
	logger := c.Logger.With().Str("component", "application.repository.get").Logger()

	if id == nil && slug == nil {
		logger.Error().Msg("either id or slug must be provided")
		return nil, fmt.Errorf("either id or slug must be provided")
	}

	var repo domain.Repository
	if id != nil {
		repo.Id = *id
	}
	if slug != nil {
		repo.Slug = *slug
	}

	repository, err := c.RepoPres.GetRepository(repo)
	if id == nil && isNotFound(err) {
		// A former slug of a renamed repository.
		if renamed, rerr := c.RepoPres.ResolveRedirect(*slug); rerr == nil {
			return renamed, nil
		}
	}
	switch helperError.Catch(err) {
	case nil:
	case gorm.ErrRecordNotFound:
		logger.Warn().Str("repo_id", repo.Id).Str("repo_slug", repo.Slug).Msg("repository not found")
		return nil, ErrRepositoryNotFound
	default:
		logger.Error().Err(err).Str("repo_id", repo.Id).Str("repo_slug", repo.Slug).Msg("failed to get repository")
		return nil, err
	}

	return repository, nil
}

// GetUserEffectiveRole returns the highest role userId has on the repository,
// directly or through one of their groups.
func (c *RepositoryApp) GetUserEffectiveRole(repositoryId, userId string) (domain.AccessType, error) {
	logger := c.Logger.With().Str("component", "application.repository.get_user_effective_role").Logger()

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

	effectiveRole := domain.AccessTypeNone
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
