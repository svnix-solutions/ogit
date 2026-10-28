package application

import (
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/infrastructure/storage"
)

// BrowseApp opens repositories for reading their content (web UI browsing,
// raw files, archives). Anyone may read public repositories; private ones
// need read access, and are not found otherwise.
type BrowseApp struct {
	Repositories *RepositoryApp
	Storage      storage.Config
	LFS          *LFSApp
}

// LFSFor serves repo's Git LFS objects to userId (nil without LFS).
func (c *BrowseApp) LFSFor(repo *domain.Repository, userId string) gitbrowse.LFSStore {
	return c.LFS.Reader(repo, userId)
}

// Open returns the repository identified by id or path and its git storer,
// if userId ("" for anonymous) may read it.
// A path ending in .wiki opens the repository's wiki (wiki_app.go).
func (c *BrowseApp) Open(id, path *string, userId string) (*domain.Repository, storer.Storer, error) {
	var wiki bool
	if path != nil {
		base, isWiki := domain.SplitWikiPath(*path)
		path, wiki = &base, isWiki
	}
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, nil, err
	}
	if wiki {
		if repo.WikiAccess == domain.WikiOff {
			return nil, nil, ErrWikiNotFound
		}
		if repo, err = c.Repositories.wikiFor(repo, false); err != nil {
			return nil, nil, ErrWikiNotFound
		}
	}
	st, err := c.Storage.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return repo, st, nil
}
