package application

import (
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/storage"
)

// BrowseApp opens repositories for reading their content (web UI browsing,
// raw files, archives). Anyone may read public repositories; private ones
// need read access, and are not found otherwise.
type BrowseApp struct {
	Repositories *RepositoryApp
	Storage      storage.Config
}

// Open returns the repository identified by id or path and its git storer,
// if userId ("" for anonymous) may read it.
func (c *BrowseApp) Open(id, path *string, userId string) (*domain.Repository, storer.Storer, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, nil, err
	}
	st, err := c.Storage.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return repo, st, nil
}
