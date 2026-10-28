package domain

import (
	"strings"

	"gorm.io/gorm"
)

// WikiAccess says who edits a repository's wiki. Readers always read it
// (unless it is off).
type WikiAccess string

const (
	WikiWriters WikiAccess = "writers" // developers and up
	WikiReaders WikiAccess = "readers" // everyone signed in who can read
	WikiOff     WikiAccess = "off"
)

func (a WikiAccess) Valid() bool {
	return a == WikiWriters || a == WikiReaders || a == WikiOff
}

// WikiSuffix ends a wiki's path (<namespace>/<slug>.wiki) and storage key.
// Slugs can't end with it: slug.Make turns dots into dashes.
const WikiSuffix = ".wiki"

// SplitWikiPath returns the repository's path for a wiki's path, and
// whether it was one.
func SplitWikiPath(path string) (string, bool) {
	return strings.CutSuffix(path, WikiSuffix)
}

// WikiOf is the repository's wiki as a repository value: same row, the
// wiki's storage key.
func WikiOf(repo *Repository) *Repository {
	w := *repo
	w.StorageKey = repo.StorageKey + WikiSuffix
	w.Wiki = true
	return &w
}

// RepositoryOfWiki is the repository a wiki value (WikiOf) belongs to.
func RepositoryOfWiki(wiki *Repository) *Repository {
	if !wiki.Wiki {
		return wiki
	}
	r := *wiki
	r.StorageKey = strings.TrimSuffix(wiki.StorageKey, WikiSuffix)
	r.Wiki = false
	return &r
}

// GitPath is how git addresses the repository (or its wiki), without .git.
func (r Repository) GitPath() string {
	if r.Wiki {
		return r.FullPath() + WikiSuffix
	}
	return r.FullPath()
}

// BeforeCreate defaults the wiki to writers for rows made without it.
func (r *Repository) BeforeCreate(*gorm.DB) error {
	if r.WikiAccess == "" {
		r.WikiAccess = WikiWriters
	}
	return nil
}
