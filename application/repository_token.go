package application

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Repository token rules:
//   - a repository token is bound to one repository; maintainers and owners
//     create, list and revoke them (its value is shown once);
//   - scopes: read:repository (clone, fetch, and read the repository's API:
//     files, pull requests, issues, milestones, statuses), write:repository
//     (push, still under branch protection: a token is never on a rule's
//     list) and write:status (post commit statuses, also through /api/v3);
//   - optional expiry; the last use is recorded (at most once a minute);
//   - on the API a token acts as "repo-token:<repository id>", a reader of
//     that repository only and never a person: it can't write through the
//     API (comments, reviews, ...), whatever its scopes.

var (
	ErrInvalidTokenRequest = errors.New("a token needs a name (at most 100 characters), at least one scope among read:repository, write:repository and write:status, and an expiry in the future if any")
	ErrTokenNameTaken      = errors.New("this repository already has a token with this name")
)

var repositoryScopes = []domain.TokenScope{domain.ScopeReadRepository, domain.ScopeWriteRepository, domain.ScopeWriteStatus, domain.ScopeReadPackages, domain.ScopeWritePackages}

type RepositoryTokenApp struct {
	Repositories *RepositoryApp
	Tokens       *TokenApp
}

func (c *RepositoryTokenApp) manage(id, path *string, actorId string) (*domain.Repository, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanAdmin() {
		return nil, ErrForbidden
	}
	return repo, nil
}

func (c *RepositoryTokenApp) List(id, path *string, actorId string) ([]domain.Token, error) {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return nil, err
	}
	return c.Tokens.TokenPers.GetByRepositoryId(repo.Id)
}

// Create returns the token's value, shown once, and the token.
func (c *RepositoryTokenApp) Create(id, path *string, actorId, name, description string, scopes []domain.TokenScope, expiresAt *time.Time) (string, *domain.Token, error) {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return "", nil, err
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 || len(scopes) == 0 || (expiresAt != nil && expiresAt.Before(time.Now())) {
		return "", nil, ErrInvalidTokenRequest
	}
	for _, s := range scopes {
		if !slices.Contains(repositoryScopes, s) {
			return "", nil, ErrInvalidTokenRequest
		}
	}
	existing, err := c.Tokens.TokenPers.GetByRepositoryId(repo.Id)
	if err != nil {
		return "", nil, err
	}
	for _, t := range existing {
		if t.Name == name {
			return "", nil, ErrTokenNameTaken
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("failed to generate token: %w", err)
	}
	plaintext := tokenPrefix + hex.EncodeToString(secret)
	repoId := repo.Id
	token := &domain.Token{
		Name: name, Description: description, Token: HashToken(plaintext), Type: domain.TokenRepo,
		RepositoryId: &repoId, Active: true, Scopes: scopes, ExpiresAt: expiresAt, CreatedById: &actorId,
	}
	if err := c.Tokens.TokenPers.Create(token); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return "", nil, ErrTokenNameTaken
	} else if err != nil {
		return "", nil, err
	}
	return plaintext, token, nil
}

func (c *RepositoryTokenApp) Revoke(id, path *string, actorId, tokenId string) error {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return err
	}
	token, err := c.Tokens.TokenPers.GetById(tokenId)
	if isNotFound(err) || (err == nil && (token.Type != domain.TokenRepo || token.RepositoryId == nil || *token.RepositoryId != repo.Id)) {
		return ErrTokenNotFound
	} else if err != nil {
		return err
	}
	return c.Tokens.TokenPers.Delete(token)
}
