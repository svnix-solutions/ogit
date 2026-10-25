package application

import (
	"errors"
	"fmt"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
)

// Pull requests from forks:
//   - the source branch lives in a fork of the same network
//     (SourceRepositoryId); its commits are copied into the target as
//     refs/pull/<number>/head when the pull request is opened, when the fork
//     branch is pushed (SourcePushed), and when an open pull request is
//     viewed and the fork moved meanwhile;
//   - everything else (reviews, checks, merging) works on that copy, so a
//     merged or closed pull request stays readable after the fork is gone;
//   - the author opens, edits and closes it with write access to the fork
//     and read access to the target; the target's maintainers merge;
//   - with AllowMaintainerEdits (the default), the target's developers may
//     push to the source branch, and only that branch, of the fork while the
//     pull request is open (EditableBranches, used by git authorization);
//   - "delete branch" after merging does not touch the fork.

var ErrNotInNetwork = errors.New("pull requests can only come from forks of the same repository")

// sourceRef splits a repository id or path (<namespace>/<slug>) into
// GetRepositoryForUser's id and path arguments.
func sourceRef(s string) (*string, *string) {
	if validator.IsValidUUID(s) {
		return &s, nil
	}
	return nil, &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func pullHeadRef(number int) plumbing.ReferenceName {
	return plumbing.ReferenceName(fmt.Sprintf("refs/pull/%d/head", number))
}

// copyHead copies the fork's commits up to tip into the target and points
// refs/pull/<n>/head at tip.
func (c *PullRequestApp) copyHead(fork storer.Storer, target storer.Storer, pr *domain.PullRequest, tip plumbing.Hash, haves ...plumbing.Hash) error {
	ref := pullHeadRef(pr.Number)
	if cur, err := target.Reference(ref); err == nil {
		if cur.Hash() == tip {
			return nil
		}
		haves = append(haves, cur.Hash())
	}
	if _, err := helpergit.CopyObjects(fork, target, []plumbing.Hash{tip}, haves); err != nil {
		return fmt.Errorf("copying the fork's commits: %w", err)
	}
	return target.SetReference(plumbing.NewHashReference(ref, tip))
}

// headTip returns an open or closed pull request's source tip as stored in
// the target, and whether the source branch still exists. For pull
// requests from forks it first brings the copy up to date (sync).
func (c *PullRequestApp) headTip(st storer.Storer, repo *domain.Repository, pr *domain.PullRequest, sync bool) (*object.Commit, *domain.Repository, bool) {
	if pr.SourceRepositoryId == nil {
		tip, err := branchTip(st, pr.SourceBranch)
		return tip, nil, err == nil
	}
	fork, ferr := c.Repositories.GetRepository(pr.SourceRepositoryId, nil)
	exists := false
	if ferr == nil {
		if forkSt, err := c.Repositories.StorageConfig.GetStorer(fork.StorageKey); err == nil {
			if tip, err := branchTip(forkSt, pr.SourceBranch); err == nil {
				exists = true
				if sync && pr.State == domain.PullRequestOpen {
					var haves []plumbing.Hash
					if t, err := branchTip(st, pr.TargetBranch); err == nil {
						haves = append(haves, t.Hash)
					}
					if err := c.copyHead(forkSt, st, pr, tip.Hash, haves...); err != nil {
						c.Logger.Warn().Err(err).Str("component", "application.pull_request").Int("number", pr.Number).Msg("failed to sync a fork's head")
					}
				}
			}
		}
	} else {
		fork = nil
	}
	ref, err := st.Reference(pullHeadRef(pr.Number))
	if err != nil {
		return nil, fork, false
	}
	tip, err := object.GetCommit(st, ref.Hash())
	if err != nil {
		return nil, fork, false
	}
	return tip, fork, exists
}

// SourcePushed brings the pull requests whose source is a pushed branch of
// fork up to date in their targets, and sends their synchronize events.
func (c *PullRequestApp) SourcePushed(fork *domain.Repository, pusher Pusher, updates []RefUpdate) {
	if c == nil || len(updates) == 0 {
		return
	}
	prs, err := c.Pulls.OpenFrom(fork.Id)
	if err != nil || len(prs) == 0 {
		return
	}
	moved := map[string]RefUpdate{}
	for _, u := range updates {
		if u.Name.IsBranch() && !u.New.IsZero() {
			moved[u.Name.Short()] = u
		}
	}
	for i := range prs {
		pr := &prs[i]
		u, ok := moved[pr.SourceBranch]
		if !ok {
			continue
		}
		repo, err := c.Repositories.GetRepository(&pr.RepositoryId, nil)
		if err != nil {
			continue
		}
		st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
		if err != nil {
			continue
		}
		tip, _, _ := c.headTip(st, repo, pr, true)
		if tip == nil {
			continue
		}
		view, err := c.Get(&repo.Id, nil, pusher.UserId, pr.Number)
		if err != nil {
			view = &PullRequestView{PullRequest: pr, Repository: repo, SourceRepository: fork}
		}
		c.Webhooks.PullRequestEvent(view, "synchronize", pusher.UserId, PullRequestPayload{Before: u.Old.String(), After: u.New.String()})
	}
}

// EditableBranches returns the branches of fork that userId may push to as
// a maintainer of open pull requests' targets (AllowMaintainerEdits, write
// access to the target).
func (c *PullRequestApp) EditableBranches(fork *domain.Repository, userId string) []string {
	if c == nil || userId == "" {
		return nil
	}
	prs, err := c.Pulls.OpenFrom(fork.Id)
	if err != nil {
		return nil
	}
	var out []string
	for _, pr := range prs {
		if !pr.AllowMaintainerEdits || slices.Contains(out, pr.SourceBranch) {
			continue
		}
		role, err := c.Repositories.GetUserEffectiveRole(pr.RepositoryId, userId)
		if err == nil && role.CanWrite() {
			out = append(out, pr.SourceBranch)
		}
	}
	return out
}

// SourceName returns "<namespace>/<slug>" of a pull request's fork ("" for
// this repository, and "deleted fork" when it is gone), for lists.
func (c *PullRequestApp) SourceName(pr *domain.PullRequest) string {
	if pr.SourceRepositoryId == nil {
		return ""
	}
	fork, err := c.Repositories.GetRepository(pr.SourceRepositoryId, nil)
	if err != nil {
		return "deleted fork"
	}
	return fork.FullPath()
}

// Preview compares a fork's branch (or this repository's, with
// sourceRepo "") with target, before a pull request exists, without
// copying anything.
func (c *PullRequestApp) Preview(id, path *string, userId, sourceRepo, source, target string) (*gitbrowse.Comparison, error) {
	repo, st, _, err := c.repo(id, path, userId)
	if err != nil {
		return nil, err
	}
	srcSt := storer.Storer(st)
	if sourceRepo != "" {
		fid, fpath := sourceRef(sourceRepo)
		fork, err := c.Repositories.GetRepositoryForUser(fid, fpath, userId)
		if err != nil {
			return nil, err
		}
		if fork.Id != repo.Id {
			if !c.Repositories.sameNetwork(fork, repo) {
				return nil, ErrNotInNetwork
			}
			if srcSt, err = c.Repositories.StorageConfig.GetStorer(fork.StorageKey); err != nil {
				return nil, err
			}
		}
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	head, err := branchTip(srcSt, source)
	if err != nil {
		return nil, err
	}
	targetRef, err := st.Reference(plumbing.NewBranchReferenceName(target))
	if err != nil {
		return nil, ErrBranchNotFound
	}
	both := overlay(st, srcSt)
	base, err := object.GetCommit(both, targetRef.Hash())
	if err != nil {
		return nil, err
	}
	head, err = object.GetCommit(both, head.Hash)
	if err != nil {
		return nil, err
	}
	return gitbrowse.Compare(base, head, gitbrowse.DefaultLimits)
}
