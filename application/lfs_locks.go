package application

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
)

// lockLimit bounds a page of locks.
const lockLimit = 100

// CreateLock locks path for the caller (a person with write access).
func (c *LFSApp) CreateLock(acc *LFSAccess, path, ref string) (*domain.LFSLock, error) {
	// Wikis have no locks (the repository's are its own).
	if !acc.Write || acc.Limited || !domain.IsPerson(acc.UserId) || acc.Repo.Wiki {
		return nil, ErrGitForbidden
	}
	path, err := cleanLockPath(path)
	if err != nil {
		return nil, err
	}
	if l, err := c.Locks.ByPath(acc.Repo.Id, path); err == nil {
		return nil, &LFSLockConflict{Lock: l}
	} else if !isNotFound(err) {
		return nil, err
	}
	l := &domain.LFSLock{RepositoryId: acc.Repo.Id, Path: path, OwnerId: acc.UserId, Ref: ref}
	if err := c.Locks.Create(l); err != nil {
		// Someone locked it meanwhile (unique index).
		if other, err2 := c.Locks.ByPath(acc.Repo.Id, path); err2 == nil {
			return nil, &LFSLockConflict{Lock: other}
		}
		return nil, err
	}
	return c.Locks.Get(acc.Repo.Id, l.Id)
}

// ListLocks pages through locks (readers may list them). next is the cursor
// of the following page, "" at the end.
func (c *LFSApp) ListLocks(acc *LFSAccess, path, id, cursor string, limit int) (locks []domain.LFSLock, next string, err error) {
	if limit <= 0 || limit > lockLimit {
		limit = lockLimit
	}
	if acc.Repo.Wiki {
		return nil, "", nil
	}
	if path != "" {
		if path, err = cleanLockPath(path); err != nil {
			return nil, "", nil
		}
	}
	locks, err = c.Locks.List(acc.Repo.Id, path, id, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	if len(locks) > limit {
		next = locks[limit].Id
		locks = locks[:limit]
	}
	return locks, next, nil
}

// VerifyLocks splits a writer's view of the locks into theirs and others'.
func (c *LFSApp) VerifyLocks(acc *LFSAccess, cursor string, limit int) (ours, theirs []domain.LFSLock, next string, err error) {
	if !acc.Write {
		return nil, nil, "", ErrGitForbidden
	}
	locks, next, err := c.ListLocks(acc, "", "", cursor, limit)
	if err != nil {
		return nil, nil, "", err
	}
	ours, theirs = []domain.LFSLock{}, []domain.LFSLock{}
	for _, l := range locks {
		if l.OwnerId == acc.UserId {
			ours = append(ours, l)
		} else {
			theirs = append(theirs, l)
		}
	}
	return ours, theirs, next, nil
}

// Unlock removes a lock: the owner's, or anyone's with force for
// maintainers and up.
func (c *LFSApp) Unlock(acc *LFSAccess, id string, force bool) (*domain.LFSLock, error) {
	if !acc.Write || !domain.IsPerson(acc.UserId) {
		return nil, ErrGitForbidden
	}
	if acc.Repo.Wiki {
		return nil, ErrLFSLockNotFound
	}
	l, err := c.Locks.Get(acc.Repo.Id, id)
	if isNotFound(err) {
		return nil, ErrLFSLockNotFound
	} else if err != nil {
		return nil, err
	}
	if l.OwnerId != acc.UserId {
		if !force {
			return nil, ErrLFSLockOwner
		}
		ok, err := c.Repositories.CanUserAccess(acc.Repo.Id, acc.UserId, "admin")
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrLFSLockOwner
		}
		c.Logger.Info().Str("component", "application.lfs").Str("repository_id", acc.Repo.Id).Str("path", l.Path).
			Str("lock_owner", l.OwnerId).Str("by", acc.UserId).Msg("lock force-unlocked")
	}
	return l, c.Locks.Delete(l.Id)
}

// LockedPathError: a push changes a file someone else locked.
type LockedPathError struct {
	Path  string
	Owner string
}

func (e *LockedPathError) Error() string {
	return fmt.Sprintf("%s is locked by %s", e.Path, e.Owner)
}

// PushCheck refuses branch updates that change a path locked by someone
// other than the pusher. A new branch is compared with the default branch.
// The locks are read once per push, and nothing is read without locks.
func (c *LFSApp) PushCheck(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer) func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error {
	if c == nil || c.Locks == nil || repo.Wiki {
		return nil
	}
	var locks []domain.LFSLock
	var loadErr error
	loaded := false
	return func(name plumbing.ReferenceName, old, new plumbing.Hash) error {
		if !name.IsBranch() || new.IsZero() {
			return nil
		}
		if !loaded {
			locks, loadErr = c.Locks.All(repo.Id)
			loaded = true
		}
		if loadErr != nil {
			return loadErr
		}
		var others []domain.LFSLock
		for _, l := range locks {
			if l.OwnerId != pusher.UserId {
				others = append(others, l)
			}
		}
		if len(others) == 0 {
			return nil
		}
		if old.IsZero() && repo.DefaultBranch != "" && name.Short() != repo.DefaultBranch {
			if refs, ok := objects.(storer.ReferenceStorer); ok {
				if ref, err := refs.Reference(plumbing.NewBranchReferenceName(repo.DefaultBranch)); err == nil {
					old = ref.Hash()
				}
			}
		}
		newTree, err := commitTree(objects, new)
		if err != nil {
			return err
		}
		var oldTree *object.Tree
		if !old.IsZero() {
			if oldTree, err = commitTree(objects, old); err != nil {
				return err
			}
		}
		for _, l := range others {
			if entryAt(oldTree, l.Path) != entryAt(newTree, l.Path) {
				return &LockedPathError{Path: l.Path, Owner: l.Owner.Username}
			}
		}
		return nil
	}
}

func commitTree(objects storer.EncodedObjectStorer, h plumbing.Hash) (*object.Tree, error) {
	commit, err := object.GetCommit(objects, h)
	if err != nil {
		return nil, err
	}
	return commit.Tree()
}

// entryAt identifies what is at path ("" for nothing).
func entryAt(tree *object.Tree, path string) string {
	if tree == nil {
		return ""
	}
	e, err := tree.FindEntry(path)
	if err != nil {
		if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
			return ""
		}
		return "error:" + err.Error()
	}
	return e.Mode.String() + " " + e.Hash.String()
}

// Chain runs checks in order (nil ones skipped).
func Chain(checks ...func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error) func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error {
	return func(name plumbing.ReferenceName, old, new plumbing.Hash) error {
		for _, check := range checks {
			if check != nil {
				if err := check(name, old, new); err != nil {
					return err
				}
			}
		}
		return nil
	}
}
