package application

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
)

// Fork rules:
//   - anyone who can read a repository forks it into their own namespace or
//     a group they maintain (as for creating a repository there); the fork
//     is a full copy of its branches and tags, independent of the original;
//   - a fork of a private repository is private; the original's members get
//     no access to it (the forker owns it);
//   - forks start with issues off, as on GitHub;
//   - the forks of a repository are listed to those who can read each one;
//   - "sync" fast-forwards a fork's branch to the original's (writers of the
//     fork, if they can still read the original); a branch that has its own
//     commits is not touched (ErrDiverged).

var (
	ErrNotAFork = errors.New("this repository is not a fork")
	ErrDiverged = errors.New("the branch has commits the original doesn't have; merge or rebase it locally")
)

// Fork copies the repository into namespace ("" for the actor's own) under
// name ("" keeps the original's).
func (c *RepositoryApp) Fork(id, path *string, actorId, namespace, name string) (*domain.Repository, error) {
	if actorId == "" {
		return nil, ErrForbidden
	}
	src, err := c.GetRepositoryForUser(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if name == "" {
		name = src.Name
	}
	tmpl := &domain.Repository{Name: name, Description: src.Description, IsPrivate: src.IsPrivate, DefaultBranch: src.DefaultBranch, ForkOfId: &src.Id}
	fork, err := c.create(namespace, actorId, tmpl, func(repo *domain.Repository) error { return c.copyRepository(src, repo) })
	if err != nil {
		return nil, err
	}
	c.Webhooks.ForkEvent(src, fork, actorId)
	return fork, nil
}

// copyRepository fills dst's empty storage with src's branches, tags and
// HEAD.
func (c *RepositoryApp) copyRepository(src, dst *domain.Repository) error {
	if err := c.StorageConfig.InitRepository(dst.StorageKey); err != nil {
		return err
	}
	from, err := c.StorageConfig.GetStorer(src.StorageKey)
	if err != nil {
		return err
	}
	to, err := c.StorageConfig.GetStorer(dst.StorageKey)
	if err != nil {
		return err
	}
	iter, err := from.IterReferences()
	if err != nil {
		return err
	}
	var refs []*plumbing.Reference
	var tips []plumbing.Hash
	err = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference && (r.Name().IsBranch() || r.Name().IsTag()) {
			refs, tips = append(refs, r), append(tips, r.Hash())
		}
		return nil
	})
	if err != nil {
		return err
	}
	if _, err := helpergit.CopyObjects(from, to, tips, nil); err != nil {
		return err
	}
	for _, r := range refs {
		if err := to.SetReference(r); err != nil {
			return err
		}
	}
	if head, err := from.Reference(plumbing.HEAD); err == nil && head.Type() == plumbing.SymbolicReference {
		return to.SetReference(head)
	}
	return nil
}

// Forks returns the forks of a repository that userId can read.
func (c *RepositoryApp) Forks(id, path *string, userId string) ([]domain.Repository, error) {
	repo, err := c.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, err
	}
	forks, err := c.RepoPres.ForksOf(repo.Id)
	if err != nil {
		return nil, err
	}
	out := []domain.Repository{}
	for _, f := range forks {
		if ok, err := c.canRead(&f, userId); err == nil && ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (c *RepositoryApp) canRead(repo *domain.Repository, userId string) (bool, error) {
	if !repo.IsPrivate {
		return true, nil
	}
	if userId == "" {
		return false, nil
	}
	return c.CanUserAccess(repo.Id, userId, "read")
}

// Parent returns the repository repo was forked from, if userId can read it
// (nil otherwise, or when it isn't a fork).
func (c *RepositoryApp) Parent(repo *domain.Repository, userId string) *domain.Repository {
	if repo.ForkOfId == nil {
		return nil
	}
	parent, err := c.GetRepositoryForUser(repo.ForkOfId, nil, userId)
	if err != nil {
		return nil
	}
	return parent
}

// sameNetwork reports whether a and b are forks of a common repository
// (or one of the other).
func (c *RepositoryApp) sameNetwork(a, b *domain.Repository) bool {
	root := func(r *domain.Repository) string {
		for range 20 {
			if r.ForkOfId == nil {
				return r.Id
			}
			parent, err := c.GetRepository(r.ForkOfId, nil)
			if err != nil {
				return r.Id // the original was deleted: this is a root now
			}
			r = parent
		}
		return r.Id
	}
	return root(a) == root(b)
}

// SyncResult says what Sync did.
type SyncResult struct {
	Branch  string
	Before  string // "" when the branch was created
	After   string
	Updated bool
}

// Sync fast-forwards the fork's branch ("" for its default branch) to the
// original's.
func (c *RepositoryApp) Sync(id, path *string, actorId, branch string) (*SyncResult, error) {
	fork, role, err := c.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanWrite() {
		return nil, ErrForbidden
	}
	if fork.IsArchived {
		return nil, ErrGitArchived
	}
	if fork.ForkOfId == nil {
		return nil, ErrNotAFork
	}
	upstream, err := c.GetRepositoryForUser(fork.ForkOfId, nil, actorId)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = fork.DefaultBranch
	}
	from, err := c.StorageConfig.GetStorer(upstream.StorageKey)
	if err != nil {
		return nil, err
	}
	to, err := c.StorageConfig.GetStorer(fork.StorageKey)
	if err != nil {
		return nil, err
	}
	name := plumbing.NewBranchReferenceName(branch)
	theirs, err := from.Reference(name)
	if err != nil {
		return nil, ErrBranchNotFound
	}
	res := &SyncResult{Branch: branch, After: theirs.Hash().String()}
	ours, err := to.Reference(name)
	if err == nil {
		res.Before = ours.Hash().String()
		if ours.Hash() == theirs.Hash() {
			return res, nil // up to date
		}
		if !isAncestor(overlay(to, from), ours.Hash(), theirs.Hash()) {
			return nil, ErrDiverged
		}
	}
	var haves []plumbing.Hash
	if ours != nil {
		haves = []plumbing.Hash{ours.Hash()}
	}
	if _, err := helpergit.CopyObjects(from, to, []plumbing.Hash{theirs.Hash()}, haves); err != nil {
		return nil, fmt.Errorf("copying commits: %w", err)
	}
	if ours != nil {
		err = to.CheckAndSetReference(plumbing.NewHashReference(name, theirs.Hash()), ours)
	} else {
		err = to.SetReference(plumbing.NewHashReference(name, theirs.Hash()))
	}
	if err != nil {
		return nil, err
	}
	res.Updated = true
	var old plumbing.Hash
	if ours != nil {
		old = ours.Hash()
	}
	c.Webhooks.Pushed(fork, Pusher{UserId: actorId}, to, []RefUpdate{{Name: name, Old: old, New: theirs.Hash()}})
	return res, nil
}

// overlay reads a's objects, then b's.
func overlay(a, b storer.EncodedObjectStorer) storer.EncodedObjectStorer {
	return helpergit.Overlay{Primary: a, Secondary: b}
}
