package application

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	gitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/infrastructure/gitmerge"
	"github.com/rs/zerolog"
)

// Pull request rules:
//   - anyone who can read the repository sees its pull requests (public
//     repositories: signed out too);
//   - developers and up open them, between two branches of the repository;
//     one open pull request per source and target;
//   - the author and maintainers+ edit, close, reopen and mark them ready;
//   - maintainers+ merge (merge commit, squash or rebase), never a draft,
//     never into an archived repository, and only if the target branch
//     hasn't moved since the merge was computed (compare-and-swap);
//   - merged and closed pull requests keep their commits and diff (the
//     branch tips are recorded).

var (
	ErrPullRequestNotFound = errors.New("pull request not found")
	ErrBranchNotFound      = errors.New("branch not found")
	ErrSameBranch          = errors.New("source and target must be different branches")
	ErrPullRequestExists   = errors.New("an open pull request already exists for these branches")
	ErrNoChanges           = errors.New("the source branch has nothing the target doesn't have")
	ErrNotOpen             = errors.New("the pull request is not open")
	ErrDraft               = errors.New("draft pull requests can't be merged; mark it ready first")
	ErrTargetMoved         = errors.New("the target branch changed while merging; try again")
	ErrEmptyTitle          = errors.New("a title is required")
)

// MergeConflictError lists the files changed on both branches.
type MergeConflictError struct{ Paths []string }

func (e *MergeConflictError) Error() string {
	return "the branches conflict in " + strings.Join(e.Paths, ", ") + "; merge the target into the source branch locally first"
}

type PullRequestApp struct {
	Config        config.Config
	Logger        zerolog.Logger
	Repositories  *RepositoryApp
	Pulls         domain.PullRequestPers
	Reviews       domain.ReviewPers
	Webhooks      *WebhookApp
	Protection    *ProtectionApp // nil: no branch protection
	Statuses      *StatusApp     // required checks of protected branches
	Assignees     domain.AssigneePers
	Reviewers     domain.RequestedReviewerPers // pull_request_reviewers.go
	Milestones    domain.MilestonePers
	Issues        *IssueApp // closes "Fixes #N" issues on merge
	Labels        domain.LabelPers
	Users         domain.UserPers
	Notifications *NotificationApp // inbox and emails (notification_app.go); nil: none
}

// PullRequestView is a pull request with what can only be computed from
// git, and what the caller may do.
type PullRequestView struct {
	*domain.PullRequest
	Repository *domain.Repository
	// SourceRepository is the fork the source branch lives in (nil for this
	// repository, or when the fork was deleted).
	SourceRepository *domain.Repository
	// Open pull requests: the branches' current tips and whether they merge.
	Mergeable *bool
	Conflicts []string
	// Branches is false when a branch of an open or closed pull request no
	// longer exists.
	BranchesExist bool
	Role          domain.AccessType
	CanEdit       bool
	CanMerge      bool
	// Reviews that count (developers and up, not the author; only on the
	// head commit when the target's rule dismisses stale approvals).
	Approvals *Approvals
	// RequiredApprovals for merging: the target's rule, else the repository's.
	RequiredApprovals int
	// Protection is the rule protecting the target branch, if any.
	Protection *domain.BranchProtection
	// RequiredChecks are the target rule's required contexts and their state
	// on the head ("" when not reported).
	RequiredChecks map[string]domain.StatusState
	// MergeBlocked says why the viewer can't merge an open pull request
	// (nil when they can, or when it is not open).
	MergeBlocked error
	Labels       []domain.Label
	Assignees    []domain.User
	// RequestedReviewers have been asked to review and haven't yet.
	RequestedReviewers []domain.User
	Milestone          *domain.Milestone
	viewerId           string
}

// Viewer is who the view was computed for ("" anonymous).
func (v *PullRequestView) Viewer() string { return v.viewerId }

// repo opens a repository for userId ("" anonymous) with their role.
func (c *PullRequestApp) repo(id, path *string, userId string) (*domain.Repository, storer.Storer, domain.AccessType, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, nil, "", err
	}
	role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
	if err != nil {
		return nil, nil, "", err
	}
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, nil, "", err
	}
	return repo, st, role, nil
}

func branchTip(st storer.Storer, branch string) (*object.Commit, error) {
	ref, err := storer.ResolveReference(st, plumbing.NewBranchReferenceName(branch))
	if err != nil {
		return nil, ErrBranchNotFound
	}
	c, err := object.GetCommit(st, ref.Hash())
	if err != nil {
		return nil, ErrBranchNotFound
	}
	return c, nil
}

// NewPullRequest describes a pull request to open. SourceRepository is a
// fork in the same network (id or path; "" for this repository).
type NewPullRequest struct {
	Title, Body, Source, Target string
	SourceRepository            string
	Draft                       bool
	// AllowMaintainerEdits (pull requests from forks; default true) lets the
	// target's developers push to the source branch.
	AllowMaintainerEdits *bool
}

// Create opens a pull request; an empty target means the default branch.
// From this repository it needs developers and up; from a fork, write
// access to the fork and read access here.
func (c *PullRequestApp) Create(id, path *string, actorId string, in NewPullRequest) (*domain.PullRequest, error) {
	repo, st, role, err := c.repo(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if actorId == "" {
		return nil, ErrForbidden
	}
	if repo.IsArchived {
		return nil, ErrGitArchived
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ErrEmptyTitle
	}
	target := in.Target
	if target == "" {
		def, _, _, _ := gitbrowse.Refs(st)
		target = def
	}

	// Where the source branch lives: here, or in a fork.
	var fork *domain.Repository
	srcSt := st
	if in.SourceRepository != "" {
		fid, fpath := sourceRef(in.SourceRepository)
		fork, err = c.Repositories.GetRepositoryForUser(fid, fpath, actorId)
		if err != nil {
			return nil, err
		}
		if fork.Id == repo.Id {
			fork = nil
		}
	}
	if fork == nil {
		if !role.CanWrite() {
			return nil, ErrForbidden
		}
		if in.Source == target {
			return nil, ErrSameBranch
		}
	} else {
		forkRole, err := c.Repositories.GetUserEffectiveRole(fork.Id, actorId)
		if err != nil {
			return nil, err
		}
		if !forkRole.CanWrite() {
			return nil, ErrForbidden
		}
		if !c.Repositories.sameNetwork(fork, repo) {
			return nil, ErrNotInNetwork
		}
		if srcSt, err = c.Repositories.StorageConfig.GetStorer(fork.StorageKey); err != nil {
			return nil, err
		}
	}
	sourceTip, err := branchTip(srcSt, in.Source)
	if err != nil {
		return nil, err
	}
	targetTip, err := branchTip(st, target)
	if err != nil {
		return nil, err
	}
	// The fork's commits are not here yet: read them through an overlay.
	both := overlay(st, srcSt)
	if isAncestor(both, sourceTip.Hash, targetTip.Hash) {
		return nil, ErrNoChanges
	}
	forkId := ""
	if fork != nil {
		forkId = fork.Id
	}
	if _, err := c.Pulls.OpenFor(repo.Id, forkId, in.Source, target); err == nil {
		return nil, ErrPullRequestExists
	} else if !isNotFound(err) {
		return nil, err
	}

	pr := &domain.PullRequest{
		RepositoryId: repo.Id, Title: title, Body: in.Body, AuthorId: &actorId,
		SourceBranch: in.Source, TargetBranch: target, State: domain.PullRequestOpen, Draft: in.Draft,
	}
	if fork != nil {
		pr.SourceRepositoryId = &fork.Id
		pr.AllowMaintainerEdits = in.AllowMaintainerEdits == nil || *in.AllowMaintainerEdits
	}
	if err := c.Pulls.Create(pr); err != nil {
		return nil, err
	}
	if fork != nil {
		// Bring the fork's commits here, under refs/pull/<n>/head.
		if err := c.copyHead(srcSt, st, pr, sourceTip.Hash, targetTip.Hash); err != nil {
			return nil, err
		}
	}
	opened := *pr
	opened.HeadCommit, opened.BaseCommit = sourceTip.Hash.String(), targetTip.Hash.String()
	c.Webhooks.PullRequestEvent(&PullRequestView{PullRequest: &opened, Repository: repo, SourceRepository: fork}, "opened", actorId, PullRequestPayload{})
	c.notify(&PullRequestView{PullRequest: pr, Repository: repo}, actorId, "opened", in.Body) // watchers too, mentions or not
	return pr, nil
}

// List returns pull requests of a repository userId can read.
// List returns pull requests of a repository userId can read, optionally
// only those with a label, with each one's labels.
func (c *PullRequestApp) List(id, path *string, userId string, state domain.PullRequestState, label string, offset, limit int) ([]domain.PullRequest, map[domain.PullRequestState]int64, map[string][]domain.Label, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, nil, nil, err
	}
	labelId := ""
	if label != "" {
		labels, err := c.Labels.List(repo.Id)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, l := range labels {
			if l.Name == label {
				labelId = l.Id
			}
		}
		if labelId == "" {
			// No such label: nothing has it. (Not a fake id: Postgres
			// rejects anything but a UUID.)
			_, counts, err := c.Pulls.List(repo.Id, state, "", 0, 1) // for the counts
			return []domain.PullRequest{}, counts, map[string][]domain.Label{}, err
		}
	}
	prs, counts, err := c.Pulls.List(repo.Id, state, labelId, max(offset, 0), clampLimit(limit))
	if err != nil {
		return nil, nil, nil, err
	}
	// Open pull requests' heads are their source tips (for status icons);
	// merged and closed ones recorded theirs.
	if st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey); err == nil {
		for i := range prs {
			if prs[i].State == domain.PullRequestOpen {
				if tip, _, _ := c.headTip(st, repo, &prs[i], false); tip != nil {
					prs[i].HeadCommit = tip.Hash.String()
				}
			}
		}
	}
	ids := make([]string, len(prs))
	for i, pr := range prs {
		ids[i] = pr.Id
	}
	labels, err := c.Labels.For(ids)
	return prs, counts, labels, err
}

// Get returns a pull request with its live state.
func (c *PullRequestApp) Get(id, path *string, userId string, number int) (*PullRequestView, error) {
	repo, st, role, err := c.repo(id, path, userId)
	if err != nil {
		return nil, err
	}
	pr, err := c.Pulls.Get(repo.Id, number)
	if isNotFound(err) {
		return nil, ErrPullRequestNotFound
	} else if err != nil {
		return nil, err
	}
	view := &PullRequestView{PullRequest: pr, Repository: repo, Role: role, viewerId: userId}
	isAuthor := userId != "" && pr.AuthorId != nil && *pr.AuthorId == userId
	// Authors from forks may only read the target (public repositories:
	// without any role); getting here means they can.
	view.CanEdit = role.CanAdmin() || (isAuthor && (role.CanWrite() || pr.SourceRepositoryId != nil))

	source, fork, sourceExists := c.headTip(st, repo, pr, true)
	view.SourceRepository = fork
	target, terr := branchTip(st, pr.TargetBranch)
	view.BranchesExist = sourceExists && source != nil && terr == nil
	if view.Protection, err = c.Protection.RuleFor(repo, pr.TargetBranch); err != nil {
		return nil, err
	}
	view.RequiredApprovals = repo.RequiredApprovals
	staleBefore := ""
	if rule := view.Protection; rule != nil {
		if rule.RequiredApprovals != nil {
			view.RequiredApprovals = *rule.RequiredApprovals
		}
		if rule.DismissStaleApprovals {
			staleBefore = pr.HeadCommit
			if pr.State == domain.PullRequestOpen && source != nil {
				staleBefore = source.Hash.String()
			}
		}
	}
	if view.Approvals, err = c.approvals(repo, pr, staleBefore); err != nil {
		return nil, err
	}
	labels, err := c.Labels.For([]string{pr.Id})
	if err != nil {
		return nil, err
	}
	view.Labels = labels[pr.Id]
	if c.Assignees != nil {
		assignees, err := c.Assignees.For(domain.AssignPullRequest, []string{pr.Id})
		if err != nil {
			return nil, err
		}
		view.Assignees = assignees[pr.Id]
	}
	if c.Reviewers != nil {
		reviewers, err := c.Reviewers.For([]string{pr.Id})
		if err != nil {
			return nil, err
		}
		view.RequestedReviewers = reviewers[pr.Id]
	}
	if pr.MilestoneId != nil && c.Milestones != nil {
		if m, err := c.Milestones.Get(repo.Id, *pr.MilestoneId); err == nil {
			view.Milestone = m
		}
	}
	if pr.State == domain.PullRequestOpen && view.BranchesExist {
		pr.HeadCommit, pr.BaseCommit = source.Hash.String(), target.Hash.String()
		conflicts, upToDate, err := gitmerge.Check(st, target, source)
		if err != nil {
			return nil, err
		}
		mergeable := len(conflicts) == 0 && !upToDate
		view.Mergeable, view.Conflicts = &mergeable, conflicts
		if view.Protection != nil {
			if view.RequiredChecks, err = c.Statuses.RequiredChecks(repo, pr.HeadCommit, view.Protection.RequiredContexts); err != nil {
				return nil, err
			}
		}
		if !mergeable && len(conflicts) == 0 {
			view.MergeBlocked = ErrNoChanges
		} else if view.MergeBlocked, err = c.mergeBlocked(view, userId, false); err != nil {
			return nil, err
		}
		view.CanMerge = view.MergeBlocked == nil
	}
	return view, nil
}

// mergeBlocked returns why userId can't merge the open pull request in
// view, or nil (the second result is an unexpected error). With bypass,
// an owner's bypass of the target's rule is logged.
func (c *PullRequestApp) mergeBlocked(view *PullRequestView, userId string, bypass bool) (error, error) {
	switch {
	case !view.Role.CanAdmin():
		return ErrForbidden, nil
	case view.Draft:
		return ErrDraft, nil
	case view.Repository.IsArchived:
		return ErrGitArchived, nil
	case view.Repository.IsMirror:
		return ErrGitMirror, nil
	case !view.BranchesExist:
		return ErrBranchNotFound, nil
	case len(view.Conflicts) > 0:
		return &MergeConflictError{Paths: view.Conflicts}, nil
	}
	var violation error
	switch {
	case len(view.Approvals.ChangesRequested) > 0:
		violation = fmt.Errorf("%w by %s", ErrChangesRequested, strings.Join(view.Approvals.ChangesRequested, ", "))
	case len(view.Approvals.Approved) < view.RequiredApprovals:
		violation = fmt.Errorf("%w: %d of %d", ErrNeedsApproval, len(view.Approvals.Approved), view.RequiredApprovals)
	}
	rule := view.Protection
	if violation == nil && rule != nil && len(rule.RequiredContexts) > 0 {
		var failing []string
		for _, ctx := range rule.RequiredContexts {
			switch state := view.RequiredChecks[ctx]; state {
			case domain.StatusSuccess:
			case "":
				failing = append(failing, ctx+" (not reported)")
			default:
				failing = append(failing, ctx+" ("+string(state)+")")
			}
		}
		if len(failing) > 0 {
			violation = fmt.Errorf("%w: %s", ErrChecksRequired, strings.Join(failing, ", "))
		}
	}
	if violation == nil && rule != nil {
		ok, err := c.Protection.allowed(rule, userId)
		if err != nil {
			return nil, err
		}
		if !ok {
			violation = &ProtectedError{Branch: view.TargetBranch, Reason: "you are not allowed to merge into it"}
		}
	}
	if violation == nil {
		return nil, nil
	}
	if rule != nil {
		if !bypass {
			// Only report what a merge would do: an owner may bypass.
			if role, err := c.Repositories.GetUserEffectiveRole(view.RepositoryId, userId); err == nil && rule.OwnersBypass && role == domain.AccessTypeOwner {
				return nil, nil
			}
		} else if c.Protection.bypass(view.Repository, rule, userId, "merge", violation) {
			return nil, nil
		}
	}
	return violation, nil
}

// Compare returns a pull request's commits and diff: the source's commits
// since the merge base with the target. Merged and closed ones use the tips
// recorded then (the target as it was before the merge).
func (c *PullRequestApp) Compare(id, path *string, userId string, number int) (*gitbrowse.Comparison, error) {
	view, err := c.Get(id, path, userId, number)
	if err != nil {
		return nil, err
	}
	st, err := c.Repositories.StorageConfig.GetStorer(view.Repository.StorageKey)
	if err != nil {
		return nil, err
	}
	if view.BaseCommit == "" || view.HeadCommit == "" {
		return &gitbrowse.Comparison{Commits: []gitbrowse.Commit{}, Diff: gitbrowse.Diff{Files: []gitbrowse.FileDiff{}}}, nil
	}
	base, err := gitbrowse.GetCommit(st, view.BaseCommit)
	if err != nil {
		return nil, err
	}
	head, err := gitbrowse.GetCommit(st, view.HeadCommit)
	if err != nil {
		return nil, err
	}
	return gitbrowse.Compare(base, head, gitbrowse.DefaultLimits)
}

// Conflicts details what keeps an open pull request from merging: empty once
// it's closed, merged or has no conflicts.
func (c *PullRequestApp) Conflicts(id, path *string, userId string, number int) ([]gitmerge.ConflictFile, error) {
	view, err := c.Get(id, path, userId, number)
	if err != nil {
		return nil, err
	}
	if len(view.Conflicts) == 0 {
		return nil, nil
	}
	st, err := c.Repositories.StorageConfig.GetStorer(view.Repository.StorageKey)
	if err != nil {
		return nil, err
	}
	base, err := gitbrowse.GetCommit(st, view.BaseCommit)
	if err != nil {
		return nil, err
	}
	head, err := gitbrowse.GetCommit(st, view.HeadCommit)
	if err != nil {
		return nil, err
	}
	files, _, err := gitmerge.Conflicts(st, base, head)
	return files, err
}

// PullRequestUpdate holds the changes for Update; nil fields are kept.
type PullRequestUpdate struct {
	Title *string
	Body  *string
	Draft *bool
	// AllowMaintainerEdits: pull requests from forks, their author only.
	AllowMaintainerEdits *bool
}

func (c *PullRequestApp) Update(id, path *string, actorId string, number int, upd PullRequestUpdate) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.CanEdit {
		return nil, ErrForbidden
	}
	fields := map[string]any{"updated_at": time.Now()}
	if upd.AllowMaintainerEdits != nil {
		if view.SourceRepositoryId == nil || view.AuthorId == nil || *view.AuthorId != actorId {
			return nil, ErrForbidden
		}
		fields["allow_maintainer_edits"] = *upd.AllowMaintainerEdits
	}
	if upd.Title != nil {
		t := strings.TrimSpace(*upd.Title)
		if t == "" {
			return nil, ErrEmptyTitle
		}
		fields["title"] = t
	}
	if upd.Body != nil {
		fields["body"] = *upd.Body
	}
	if upd.Draft != nil {
		if view.State != domain.PullRequestOpen {
			return nil, ErrNotOpen
		}
		fields["draft"] = *upd.Draft
	}
	if ok, err := c.Pulls.Update(view.Id, view.State, fields); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotOpen
	}
	updated, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	changes := map[string]any{}
	if t, ok := fields["title"].(string); ok && t != view.Title {
		changes["title"] = map[string]string{"from": view.Title}
	}
	if upd.Body != nil && *upd.Body != view.Body {
		changes["body"] = map[string]string{"from": view.Body}
	}
	if len(changes) > 0 {
		c.Webhooks.PullRequestEvent(updated, "edited", actorId, PullRequestPayload{Changes: changes})
	}
	if upd.Draft != nil && *upd.Draft != view.Draft {
		action := map[bool]string{true: "converted_to_draft", false: "ready_for_review"}[*upd.Draft]
		c.Webhooks.PullRequestEvent(updated, action, actorId, PullRequestPayload{})
	}
	return updated, nil
}

// Close records the branch tips so the pull request stays readable.
func (c *PullRequestApp) Close(id, path *string, actorId string, number int) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.CanEdit {
		return nil, ErrForbidden
	}
	if view.State != domain.PullRequestOpen {
		return nil, ErrNotOpen
	}
	now := time.Now()
	fields := map[string]any{"state": domain.PullRequestClosed, "closed_at": now, "updated_at": now,
		"head_commit": view.HeadCommit, "base_commit": view.BaseCommit}
	if ok, err := c.Pulls.Update(view.Id, domain.PullRequestOpen, fields); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotOpen
	}
	c.notify(view, actorId, "closed", "")
	return c.withEvent(id, path, actorId, number, "closed")
}

func (c *PullRequestApp) Reopen(id, path *string, actorId string, number int) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.CanEdit {
		return nil, ErrForbidden
	}
	if view.State != domain.PullRequestClosed {
		return nil, ErrNotOpen
	}
	if !view.BranchesExist {
		return nil, ErrBranchNotFound
	}
	if _, err := c.Pulls.OpenFor(view.RepositoryId, deref(view.SourceRepositoryId), view.SourceBranch, view.TargetBranch); err == nil {
		return nil, ErrPullRequestExists
	}
	fields := map[string]any{"state": domain.PullRequestOpen, "closed_at": nil, "updated_at": time.Now()}
	if ok, err := c.Pulls.Update(view.Id, domain.PullRequestClosed, fields); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotOpen
	}
	c.notify(view, actorId, "reopened", "")
	return c.withEvent(id, path, actorId, number, "reopened")
}

// withEvent returns the pull request after a change and queues a
// pull_request event for it.
func (c *PullRequestApp) withEvent(id, path *string, actorId string, number int, action string) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	c.Webhooks.PullRequestEvent(view, action, actorId, PullRequestPayload{})
	return view, nil
}

// MergeOptions: Method is merge, squash or rebase; Message replaces the
// default merge or squash commit message.
type MergeOptions struct {
	Method       string
	Message      string
	DeleteBranch bool
}

func (c *PullRequestApp) Merge(id, path *string, actorId string, number int, opts MergeOptions) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.Role.CanAdmin() {
		return nil, ErrForbidden
	}
	if view.State != domain.PullRequestOpen {
		return nil, ErrNotOpen
	}
	if blocked, err := c.mergeBlocked(view, actorId, true); err != nil {
		return nil, err
	} else if blocked != nil {
		return nil, blocked
	}
	merger, err := c.Users.GetById(actorId)
	if err != nil {
		return nil, err
	}
	st, err := c.Repositories.StorageConfig.GetStorer(view.Repository.StorageKey)
	if err != nil {
		return nil, err
	}
	base, err := gitbrowse.GetCommit(st, view.BaseCommit)
	if err != nil {
		return nil, err
	}
	head, err := gitbrowse.GetCommit(st, view.HeadCommit)
	if err != nil {
		return nil, err
	}

	message := opts.Message
	if message == "" {
		message = defaultMergeMessage(view.PullRequest, opts.Method)
	}
	res, err := gitmerge.Merge(st, gitmerge.Request{
		Base: base, Head: head, Method: opts.Method, Message: message,
		Committer: object.Signature{Name: merger.Username, Email: merger.Email, When: time.Now()},
	})
	var conflict *gitmerge.ConflictError
	switch {
	case errors.As(err, &conflict):
		return nil, &MergeConflictError{Paths: conflict.Paths}
	case errors.Is(err, gitmerge.ErrNothingToMerge):
		return nil, ErrNoChanges
	case err != nil:
		return nil, err
	}
	if err := gitmerge.Write(st, res); err != nil {
		return nil, err
	}

	// Move the target only if nobody pushed to it meanwhile.
	targetRef := plumbing.NewBranchReferenceName(view.TargetBranch)
	err = st.CheckAndSetReference(plumbing.NewHashReference(targetRef, res.Commit), plumbing.NewHashReference(targetRef, base.Hash))
	if err != nil {
		if errors.Is(err, gitstorage.ErrReferenceHasChanged) {
			return nil, ErrTargetMoved
		}
		return nil, err
	}

	now := time.Now()
	fields := map[string]any{
		"state": domain.PullRequestMerged, "merged_at": now, "merged_by_id": actorId, "updated_at": now,
		"merge_commit": res.Commit.String(), "merge_method": opts.Method,
		"head_commit": head.Hash.String(), "base_commit": base.Hash.String(),
	}
	if _, err := c.Pulls.Update(view.Id, domain.PullRequestOpen, fields); err != nil {
		return nil, err
	}

	c.notify(view, actorId, "merged", "")
	// A merge is a push to the target (and to the source, when deleted).
	pushed := []RefUpdate{{Name: targetRef, Old: base.Hash, New: res.Commit}}
	if opts.DeleteBranch && view.SourceRepositoryId == nil { // never in a fork
		if err := c.Protection.CheckDelete(view.Repository, actorId, view.SourceBranch, head.Hash); err != nil {
			c.Logger.Info().Err(err).Str("component", "application.pull_request").Str("branch", view.SourceBranch).Msg("protected source branch kept")
		} else if err := deleteBranch(st, view.SourceBranch, head.Hash); err != nil {
			// The merge happened; a branch that moved is kept.
			c.Logger.Warn().Err(err).Str("component", "application.pull_request").Str("branch", view.SourceBranch).Msg("source branch not deleted")
		} else {
			pushed = append(pushed, RefUpdate{Name: plumbing.NewBranchReferenceName(view.SourceBranch), Old: head.Hash})
		}
	}
	c.Webhooks.Pushed(view.Repository, Pusher{UserId: actorId}, st, pushed)
	if view.TargetBranch == view.Repository.DefaultBranch {
		c.Issues.CloseFromPull(view.Repository, view.PullRequest, actorId)
	}
	return c.withEvent(id, path, actorId, number, "closed") // merged: true
}

func defaultMergeMessage(pr *domain.PullRequest, method string) string {
	if method == gitmerge.MethodSquash {
		msg := fmt.Sprintf("%s (#%d)", pr.Title, pr.Number)
		if strings.TrimSpace(pr.Body) != "" {
			msg += "\n\n" + strings.TrimSpace(pr.Body)
		}
		return msg + "\n"
	}
	return fmt.Sprintf("Merge pull request #%d from %s\n\n%s\n", pr.Number, pr.SourceBranch, pr.Title)
}

// deleteBranch removes branch if it still points at tip.
func deleteBranch(st storer.Storer, branch string, tip plumbing.Hash) error {
	name := plumbing.NewBranchReferenceName(branch)
	if r, ok := st.(interface {
		RemoveReferenceIf(plumbing.ReferenceName, plumbing.Hash) error
	}); ok {
		return r.RemoveReferenceIf(name, tip)
	}
	current, err := st.Reference(name)
	if err != nil {
		return err
	}
	if current.Hash() != tip {
		return gitstorage.ErrReferenceHasChanged
	}
	return st.RemoveReference(name)
}

// writer returns the pull request if actorId may set its labels,
// assignees and milestone (developers and up, repository not archived).
func (c *PullRequestApp) writer(id, path *string, actorId string, number int) (*PullRequestView, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.Role.CanWrite() {
		return nil, ErrForbidden
	}
	if view.Repository.IsArchived {
		return nil, ErrGitArchived
	}
	return view, nil
}

// SetAssignees replaces a pull request's assignees by username.
func (c *PullRequestApp) SetAssignees(id, path *string, actorId string, number int, names []string) ([]domain.User, error) {
	view, err := c.writer(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	users, err := c.Repositories.assignees(view.Repository, names)
	if err != nil {
		return nil, err
	}
	if err := c.Assignees.Set(domain.AssignPullRequest, view.Id, userIds(users)); err != nil {
		return nil, err
	}
	added, removed := assignmentDiff(view.Assignees, users)
	for _, u := range added {
		c.Webhooks.PullRequestEvent(view, "assigned", actorId, PullRequestPayload{Assignee: c.Webhooks.hookUserOrNil(u)})
	}
	for _, u := range removed {
		c.Webhooks.PullRequestEvent(view, "unassigned", actorId, PullRequestPayload{Assignee: c.Webhooks.hookUserOrNil(u)})
	}
	if len(added) > 0 {
		c.notify(view, actorId, "assigned "+usernames(added)+" to", "")
	}
	if users == nil {
		users = []domain.User{}
	}
	return users, nil
}

// SetMilestone sets a pull request's milestone ("" removes it).
func (c *PullRequestApp) SetMilestone(id, path *string, actorId string, number int, milestoneId string) (*PullRequestView, error) {
	view, err := c.writer(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	var value any
	if milestoneId != "" {
		m, err := c.Milestones.Get(view.RepositoryId, milestoneId)
		if err != nil {
			return nil, ErrMilestoneNotFound
		}
		value = m.Id
	}
	if _, err := c.Pulls.Update(view.Id, view.State, map[string]any{"milestone_id": value, "updated_at": time.Now()}); err != nil {
		return nil, err
	}
	nv, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	old := view.MilestoneId
	switch {
	case nv.MilestoneId != nil && (old == nil || *old != *nv.MilestoneId):
		c.Webhooks.PullRequestEvent(nv, "milestoned", actorId, PullRequestPayload{Milestone: c.Webhooks.milestone(nv.Repository, nv.MilestoneId)})
	case nv.MilestoneId == nil && old != nil:
		c.Webhooks.PullRequestEvent(nv, "demilestoned", actorId, PullRequestPayload{Milestone: c.Webhooks.milestone(nv.Repository, old)})
	}
	return nv, nil
}
