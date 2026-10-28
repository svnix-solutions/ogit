package application

import (
	"errors"
	"strings"
	"time"

	gslug "github.com/gosimple/slug"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Repository lifecycle rules:
//   - owners rename (the slug follows the name; the old path keeps working
//     through a redirect until another repository takes it), change
//     visibility, archive/unarchive and delete;
//   - maintainers and owners edit the description and the wiki's access
//     (application/wiki_app.go);
//   - an archived repository refuses every push (ErrGitArchived), owners'
//     included; reads keep working;
//   - deleting needs the slug repeated and removes the git data.

var (
	ErrRepositoryNameTaken = errors.New("this namespace already has a repository with this name")
	ErrInvalidName         = errors.New("name must produce a non-empty slug")
	ErrGitArchived         = errors.New("repository is archived")
	// ErrGitMirror: a pull mirror follows its remote and refuses pushes
	// until mirroring stops.
	ErrGitMirror = errors.New("repository is a mirror (read-only): stop mirroring to push to it")
)

// RepositoryUpdate holds the changes for Update; nil fields are left alone.
type RepositoryUpdate struct {
	Name        *string
	Description *string
	IsPrivate   *bool
	Archived    *bool
	// RequiredApprovals for merging pull requests (owners; 0 to 10).
	RequiredApprovals *int
	// HasIssues turns issues on or off (owners).
	HasIssues *bool
	// WikiAccess: who edits the wiki, or off (maintainers and owners).
	WikiAccess *domain.WikiAccess
}

func (c *RepositoryApp) Update(id, slug *string, actorId string, upd RepositoryUpdate) (*domain.Repository, error) {
	repo, role, err := c.repositoryForManager(id, slug, actorId)
	if err != nil {
		return nil, err
	}

	ownerOnly := upd.Name != nil || upd.IsPrivate != nil || upd.Archived != nil || upd.RequiredApprovals != nil || upd.HasIssues != nil
	if ownerOnly && role != domain.AccessTypeOwner {
		return nil, ErrForbidden
	}
	if (upd.Description != nil || upd.WikiAccess != nil) && !role.CanAdmin() {
		return nil, ErrForbidden
	}
	if upd.WikiAccess != nil && !upd.WikiAccess.Valid() {
		return nil, ErrInvalidWikiMode
	}

	// Repository events, sent once everything is saved.
	type event struct {
		action  string
		changes map[string]any
	}
	var events []event

	if upd.Name != nil && *upd.Name != repo.Name {
		name := strings.TrimSpace(*upd.Name)
		newSlug := gslug.Make(name)
		if newSlug == "" {
			return nil, ErrInvalidName
		}
		if err := groupAtPath(c.GroupPers, repo.Namespace, newSlug); err != nil {
			return nil, err
		}
		if err := c.RepoPres.Rename(repo.Id, repo.Namespace, name, newSlug, repo.Slug); helperError.Catch(err) == gorm.ErrDuplicatedKey {
			return nil, ErrRepositoryNameTaken
		} else if err != nil {
			return nil, err
		}
		events = append(events, event{"renamed", map[string]any{"repository": map[string]any{"name": map[string]string{"from": repo.Slug}}}})
		repo.Name, repo.Slug = name, newSlug
	}

	fields := map[string]any{}
	if upd.Description != nil {
		if *upd.Description != repo.Description {
			events = append(events, event{"edited", map[string]any{"description": map[string]string{"from": repo.Description}}})
		}
		fields["description"] = *upd.Description
		repo.Description = *upd.Description
	}
	if upd.RequiredApprovals != nil {
		if *upd.RequiredApprovals < 0 || *upd.RequiredApprovals > 10 {
			return nil, ErrInvalidApprovals
		}
		fields["required_approvals"] = *upd.RequiredApprovals
		repo.RequiredApprovals = *upd.RequiredApprovals
	}
	if upd.HasIssues != nil {
		fields["has_issues"] = *upd.HasIssues
		repo.HasIssues = *upd.HasIssues
	}
	if upd.WikiAccess != nil {
		fields["wiki_access"] = string(*upd.WikiAccess)
		repo.WikiAccess = *upd.WikiAccess
	}
	if upd.IsPrivate != nil {
		if *upd.IsPrivate != repo.IsPrivate {
			events = append(events, event{map[bool]string{true: "privatized", false: "publicized"}[*upd.IsPrivate], nil})
		}
		fields["is_private"] = *upd.IsPrivate
		repo.IsPrivate = *upd.IsPrivate
	}
	if upd.Archived != nil && *upd.Archived != repo.IsArchived {
		fields["is_archived"] = *upd.Archived
		repo.IsArchived = *upd.Archived
		events = append(events, event{map[bool]string{true: "archived", false: "unarchived"}[*upd.Archived], nil})
		if *upd.Archived {
			now := time.Now()
			fields["archived_at"], repo.ArchivedAt = now, &now
		} else {
			fields["archived_at"], repo.ArchivedAt = nil, nil
		}
	}
	if len(fields) > 0 {
		if err := c.RepoPres.Update(repo.Id, fields); err != nil {
			return nil, err
		}
	}
	for _, e := range events {
		c.Webhooks.RepositoryEvent(repo, e.action, actorId, e.changes)
	}
	return repo, nil
}

// Delete permanently deletes a repository the actor owns. confirm must
// repeat its current slug.
func (c *RepositoryApp) Delete(id, slug *string, actorId, confirm string) error {
	repo, role, err := c.repositoryForManager(id, slug, actorId)
	if err != nil {
		return err
	}
	if role != domain.AccessTypeOwner {
		return ErrForbidden
	}
	if confirm != repo.Slug {
		return ErrConfirmMismatch
	}
	return purgeRepository(c.RepoPres, c.StorageConfig, c.Logger, repo)
}

// ListPublic returns public repositories matching query.
func (c *RepositoryApp) ListPublic(query string, limit, offset int) ([]domain.Repository, int64, error) {
	return c.RepoPres.ListPublic(query, clampLimit(limit), max(offset, 0))
}

// purgeRepository deletes a repository's rows, then its git data. Database
// first: if removing the data then fails, the repository is already gone
// for users and the leftover data is only logged.
func purgeRepository(pers domain.RepositoryPers, store storage.Config, logger zerolog.Logger, repo *domain.Repository) error {
	if err := pers.Purge(repo.Id); err != nil {
		return err
	}
	for _, key := range []string{repo.StorageKey, domain.WikiOf(repo).StorageKey} {
		if key != repo.StorageKey && !store.RepositoryExists(key) {
			continue // no wiki
		}
		if err := store.DeleteRepository(key); err != nil {
			logger.Error().Err(err).Str("component", "application.repository").Str("repository", repo.FullPath()).Str("storage_key", key).
				Msg("repository deleted but its git data could not be removed")
		}
	}
	return nil
}
