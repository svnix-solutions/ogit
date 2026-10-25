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
	"github.com/labbs/ogit/infrastructure/mail"
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
	Config       config.Config
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Pulls        domain.PullRequestPers
	Reviews      domain.ReviewPers
	Labels       domain.LabelPers
	Users        domain.UserPers
	Mailer       mail.Sender // nil or without smtp.host: no emails
}

// PullRequestView is a pull request with what can only be computed from
// git, and what the caller may do.
type PullRequestView struct {
	*domain.PullRequest
	Repository *domain.Repository
	// Open pull requests: the branches' current tips and whether they merge.
	Mergeable *bool
	Conflicts []string
	// Branches is false when a branch of an open or closed pull request no
	// longer exists.
	BranchesExist bool
	Role          domain.AccessType
	CanEdit       bool
	CanMerge      bool
	// Reviews that count (developers and up, not the author).
	Approvals *Approvals
	Labels    []domain.Label
	viewerId  string
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

// Create opens a pull request; an empty target means the default branch.
func (c *PullRequestApp) Create(id, path *string, actorId, title, body, source, target string, draft bool) (*domain.PullRequest, error) {
	repo, st, role, err := c.repo(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanWrite() {
		return nil, ErrForbidden
	}
	if repo.IsArchived {
		return nil, ErrGitArchived
	}
	if title = strings.TrimSpace(title); title == "" {
		return nil, ErrEmptyTitle
	}
	if target == "" {
		def, _, _, _ := gitbrowse.Refs(st)
		target = def
	}
	if source == target {
		return nil, ErrSameBranch
	}
	sourceTip, err := branchTip(st, source)
	if err != nil {
		return nil, err
	}
	targetTip, err := branchTip(st, target)
	if err != nil {
		return nil, err
	}
	if ok, err := sourceTip.IsAncestor(targetTip); err != nil {
		return nil, err
	} else if ok {
		return nil, ErrNoChanges
	}
	if _, err := c.Pulls.OpenFor(repo.Id, source, target); err == nil {
		return nil, ErrPullRequestExists
	} else if !isNotFound(err) {
		return nil, err
	}

	pr := &domain.PullRequest{
		RepositoryId: repo.Id, Title: title, Body: body, AuthorId: &actorId,
		SourceBranch: source, TargetBranch: target, State: domain.PullRequestOpen, Draft: draft,
	}
	if err := c.Pulls.Create(pr); err != nil {
		return nil, err
	}
	if len(Mentions(body)) > 0 {
		c.notify(&PullRequestView{PullRequest: pr, Repository: repo}, actorId, "opened", body)
	}
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
	view.CanEdit = role.CanAdmin() || (isAuthor && role.CanWrite())

	source, serr := branchTip(st, pr.SourceBranch)
	target, terr := branchTip(st, pr.TargetBranch)
	view.BranchesExist = serr == nil && terr == nil
	if view.Approvals, err = c.approvals(repo, pr); err != nil {
		return nil, err
	}
	labels, err := c.Labels.For([]string{pr.Id})
	if err != nil {
		return nil, err
	}
	view.Labels = labels[pr.Id]
	if pr.State == domain.PullRequestOpen && view.BranchesExist {
		pr.HeadCommit, pr.BaseCommit = source.Hash.String(), target.Hash.String()
		conflicts, upToDate, err := gitmerge.Check(st, target, source)
		if err != nil {
			return nil, err
		}
		mergeable := len(conflicts) == 0 && !upToDate
		view.Mergeable, view.Conflicts = &mergeable, conflicts
		view.CanMerge = mergeable && role.CanAdmin() && !pr.Draft && !repo.IsArchived && view.approved()
	}
	return view, nil
}

// approved: enough approvals and nobody asking for changes.
func (v *PullRequestView) approved() bool {
	return len(v.Approvals.Approved) >= v.Repository.RequiredApprovals && len(v.Approvals.ChangesRequested) == 0
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

// PullRequestUpdate holds the changes for Update; nil fields are kept.
type PullRequestUpdate struct {
	Title *string
	Body  *string
	Draft *bool
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
	return c.Get(id, path, actorId, number)
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
	return c.Get(id, path, actorId, number)
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
	if _, err := c.Pulls.OpenFor(view.RepositoryId, view.SourceBranch, view.TargetBranch); err == nil {
		return nil, ErrPullRequestExists
	}
	fields := map[string]any{"state": domain.PullRequestOpen, "closed_at": nil, "updated_at": time.Now()}
	if ok, err := c.Pulls.Update(view.Id, domain.PullRequestClosed, fields); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotOpen
	}
	c.notify(view, actorId, "reopened", "")
	return c.Get(id, path, actorId, number)
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
	switch {
	case !view.Role.CanAdmin():
		return nil, ErrForbidden
	case view.State != domain.PullRequestOpen:
		return nil, ErrNotOpen
	case view.Draft:
		return nil, ErrDraft
	case view.Repository.IsArchived:
		return nil, ErrGitArchived
	case !view.BranchesExist:
		return nil, ErrBranchNotFound
	case len(view.Conflicts) > 0:
		return nil, &MergeConflictError{Paths: view.Conflicts}
	case len(view.Approvals.ChangesRequested) > 0:
		return nil, fmt.Errorf("%w by %s", ErrChangesRequested, strings.Join(view.Approvals.ChangesRequested, ", "))
	case len(view.Approvals.Approved) < view.Repository.RequiredApprovals:
		return nil, fmt.Errorf("%w: %d of %d", ErrNeedsApproval, len(view.Approvals.Approved), view.Repository.RequiredApprovals)
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
	if opts.DeleteBranch {
		if err := deleteBranch(st, view.SourceBranch, head.Hash); err != nil {
			// The merge happened; a branch that moved is kept.
			c.Logger.Warn().Err(err).Str("component", "application.pull_request").Str("branch", view.SourceBranch).Msg("source branch not deleted")
		}
	}
	return c.Get(id, path, actorId, number)
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
