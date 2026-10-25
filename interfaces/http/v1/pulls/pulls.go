// Package pulls is the pull request API under
// /api/v1/repository/:identifier/pulls. Reading works without a session on
// public repositories; the rules are in application/pull_request_app.go.
package pulls

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	Pulls      *application.PullRequestApp
}

type ListRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	State      string `query:"state" json:"-"` // open (default), closed, merged or all
	Label      string `query:"label" json:"-"` // only pull requests with this label
	Offset     int    `query:"offset" json:"-"`
	Limit      int    `query:"limit" json:"-"`
}

type Summary struct {
	Number       int        `json:"number"`
	Title        string     `json:"title"`
	State        string     `json:"state"`
	Draft        bool       `json:"draft"`
	Author       string     `json:"author"` // "" once the account is deleted
	SourceBranch string     `json:"source_branch"`
	TargetBranch string     `json:"target_branch"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	MergedAt     *time.Time `json:"merged_at,omitempty"`
	ClosedAt     *time.Time `json:"closed_at,omitempty"`
	Labels       []Label    `json:"labels"`
	HeadCommit   string     `json:"head_commit,omitempty"` // the source tip (recorded once merged or closed)
	// SourceRepository is the fork the source branch is in ("" for this
	// repository, "deleted fork" when it is gone).
	SourceRepository string `json:"source_repository,omitempty"`
}

type Label struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func labels(ls []domain.Label) []Label {
	out := []Label{}
	for _, l := range ls {
		out = append(out, Label{Id: l.Id, Name: l.Name, Color: l.Color, Description: l.Description})
	}
	return out
}

type ListResponse struct {
	PullRequests []Summary        `json:"pull_requests"`
	Counts       map[string]int64 `json:"counts"` // per state
}

type CreateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Title      string `json:"title" validate:"required"`
	Body       string `json:"body"`
	Source     string `json:"source" validate:"required"`
	Target     string `json:"target"` // default branch when empty
	Draft      bool   `json:"draft"`
	// SourceRepository is the fork the source branch is in (<owner>/<repo>
	// or an id; empty for this repository).
	SourceRepository string `json:"source_repository"`
	// AllowMaintainerEdits (from forks; default true) lets this repository's
	// developers push to the source branch.
	AllowMaintainerEdits *bool `json:"allow_maintainer_edits"`
}

type NumberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
}

type UpdateRequest struct {
	Identifier string  `path:"identifier" json:"-" validate:"required"`
	Number     string  `path:"number" json:"-" validate:"required"`
	Title      *string `json:"title"`
	Body       *string `json:"body"`
	Draft      *bool   `json:"draft"` // false marks a draft ready
	// AllowMaintainerEdits: from forks, the author only.
	AllowMaintainerEdits *bool `json:"allow_maintainer_edits"`
}

type PreviewRequest struct {
	Identifier       string `path:"identifier" json:"-" validate:"required"`
	SourceRepository string `query:"source_repository" json:"-"` // a fork (<owner>/<repo>); empty for this repository
	Source           string `query:"source" json:"-" validate:"required"`
	Target           string `query:"target" json:"-"` // default branch when empty
}

type MergeRequest struct {
	Identifier   string `path:"identifier" json:"-" validate:"required"`
	Number       string `path:"number" json:"-" validate:"required"`
	Method       string `json:"method" validate:"required"` // merge, squash or rebase
	Message      string `json:"message"`
	DeleteBranch bool   `json:"delete_branch"`
}

type Detail struct {
	Summary
	Body          string   `json:"body"`
	BaseCommit    string   `json:"base_commit"`
	HeadCommit    string   `json:"head_commit"`
	MergeCommit   string   `json:"merge_commit,omitempty"`
	MergeMethod   string   `json:"merge_method,omitempty"`
	MergedBy      string   `json:"merged_by,omitempty"`
	Mergeable     *bool    `json:"mergeable,omitempty"` // open pull requests
	Conflicts     []string `json:"conflicts"`
	BranchesExist bool     `json:"branches_exist"`
	CanEdit       bool     `json:"can_edit"`
	CanMerge      bool     `json:"can_merge"`
	CanReview     bool     `json:"can_review"` // approve or request changes (not the author)
	// Approved and ChangesRequested list the reviews that count: by
	// developers and up, not the author, each reviewer's latest.
	Approved          []string `json:"approved"`
	ChangesRequested  []string `json:"changes_requested"`
	RequiredApprovals int      `json:"required_approvals"`
	// ProtectedBy is the pattern of the rule protecting the target branch.
	ProtectedBy string `json:"protected_by,omitempty"`
	// RequiredChecks are the protecting rule's required status contexts with
	// their state on the head ("" when not reported).
	RequiredChecks []RequiredCheck `json:"required_checks"`
	// AllowMaintainerEdits: pull requests from forks; this repository's
	// developers may push to the source branch.
	AllowMaintainerEdits bool `json:"allow_maintainer_edits"`
	// MergeBlocked says why the caller can't merge an open pull request.
	MergeBlocked *MergeBlocked `json:"merge_blocked,omitempty"`
	Assignees    []string      `json:"assignees"` // usernames
	Milestone    *Milestone    `json:"milestone,omitempty"`
}

type RequiredCheck struct {
	Context string `json:"context"`
	State   string `json:"state"`
}

type MergeBlocked struct {
	Type    string `json:"type"` // as in API errors: NEEDS_APPROVAL, BRANCH_PROTECTED, ...
	Message string `json:"message"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Pull requests"}
	public := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled", Description: desc + " No session needed for public repositories."}
	}
	private := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	fiberoapi.Get(repoGrp, "/:identifier/pulls", c.List, public("List pull requests", "pulls.list", "?state=open (default), closed, merged or all; newest first, with counts per state."))
	fiberoapi.Post(repoGrp, "/:identifier/pulls", c.Create, private("Open a pull request", "pulls.create", "Developers and up. From source into target (the default branch when empty), between branches of this repository."))
	fiberoapi.Get(repoGrp, "/:identifier/pulls/preview", c.Preview, public("Preview a pull request", "pulls.preview", "Commits and diff of ?source= (in ?source_repository=, a fork, or this repository) against ?target=, before opening it."))
	fiberoapi.Get(repoGrp, "/:identifier/pulls/:number", c.Get, public("Get a pull request", "pulls.get", "With mergeability and conflicting files while open, and what the caller may do."))
	fiberoapi.Patch(repoGrp, "/:identifier/pulls/:number", c.Update, private("Edit a pull request", "pulls.update", "The author and maintainers: title, body, draft (false marks it ready)."))
	fiberoapi.Post(repoGrp, "/:identifier/pulls/:number/close", c.Close, private("Close a pull request", "pulls.close", "The author and maintainers."))
	fiberoapi.Post(repoGrp, "/:identifier/pulls/:number/reopen", c.Reopen, private("Reopen a pull request", "pulls.reopen", "The author and maintainers, while both branches exist."))
	fiberoapi.Post(repoGrp, "/:identifier/pulls/:number/merge", c.Merge, private("Merge a pull request", "pulls.merge", "Maintainers and owners. method: merge, squash or rebase. Refused for drafts, conflicts, archived repositories, or if the target branch moves meanwhile."))
	setupReviews(c, repoGrp)
	setupAssignees(c, repoGrp)
	fiberoapi.Get(repoGrp, "/:identifier/pulls/:number/compare", c.Compare, public("Commits and diff", "pulls.compare", "The source's commits since the merge base and the diff, as for compare; recorded tips once merged or closed."))
}

func (c Controller) userId(ctx *fiber.Ctx) string { return optauth.UserID(ctx, c.SessionApp) }

// number parses the :number path parameter (fiber-oapi fills strings only).
func number(s string) (int, *fiberoapi.ErrorResponse) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, apierror.From(application.ErrPullRequestNotFound, zerolog.Nop())
	}
	return n, nil
}

func summary(pr *domain.PullRequest) Summary {
	s := Summary{
		Number: pr.Number, Title: pr.Title, State: string(pr.State), Draft: pr.Draft,
		SourceBranch: pr.SourceBranch, TargetBranch: pr.TargetBranch,
		CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt, MergedAt: pr.MergedAt, ClosedAt: pr.ClosedAt, HeadCommit: pr.HeadCommit,
	}
	if pr.Author != nil {
		s.Author = pr.Author.Username
	}
	s.Labels = []Label{}
	return s
}

func detail(v *application.PullRequestView) *Detail {
	d := &Detail{
		Summary: summary(v.PullRequest), AllowMaintainerEdits: v.AllowMaintainerEdits, Body: v.Body, BaseCommit: v.BaseCommit, HeadCommit: v.HeadCommit,
		MergeCommit: v.MergeCommit, MergeMethod: v.MergeMethod, Mergeable: v.Mergeable, Conflicts: v.Conflicts,
		BranchesExist: v.BranchesExist, CanEdit: v.CanEdit, CanMerge: v.CanMerge,
		Approved: v.Approvals.Approved, ChangesRequested: v.Approvals.ChangesRequested, RequiredApprovals: v.RequiredApprovals,
	}
	d.Assignees, d.Milestone = usernames(v.Assignees), toMilestone(v.Milestone)
	if v.SourceRepositoryId != nil {
		d.SourceRepository = "deleted fork"
		if v.SourceRepository != nil {
			d.SourceRepository = v.SourceRepository.FullPath()
		}
	}
	d.RequiredChecks = []RequiredCheck{}
	if v.Protection != nil {
		d.ProtectedBy = v.Protection.Pattern
		for _, ctx := range v.Protection.RequiredContexts {
			d.RequiredChecks = append(d.RequiredChecks, RequiredCheck{Context: ctx, State: string(v.RequiredChecks[ctx])})
		}
	}
	if v.MergeBlocked != nil {
		e := apierror.From(v.MergeBlocked, zerolog.Nop())
		d.MergeBlocked = &MergeBlocked{Type: e.Type, Message: v.MergeBlocked.Error()}
	}
	d.Labels = labels(v.Labels)
	d.CanReview = v.Role.CanRead() && v.State == domain.PullRequestOpen && !v.Repository.IsArchived && v.Viewer() != "" &&
		(v.AuthorId == nil || *v.AuthorId != v.Viewer())
	if d.Conflicts == nil {
		d.Conflicts = []string{}
	}
	if v.MergedBy != nil {
		d.MergedBy = v.MergedBy.Username
	}
	return d
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	state := domain.PullRequestState(req.State)
	switch req.State {
	case "":
		state = domain.PullRequestOpen
	case "all":
		state = ""
	case "open", "closed", "merged":
	default:
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusBadRequest, Details: "state must be open, closed, merged or all", Type: "INVALID_STATE"}
	}
	id, path := repoid.Split(req.Identifier)
	prs, counts, prLabels, err := c.Pulls.List(id, path, c.userId(ctx), state, req.Label, req.Offset, req.Limit)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ListResponse{PullRequests: []Summary{}, Counts: map[string]int64{"open": 0, "closed": 0, "merged": 0}}
	for i := range prs {
		s := summary(&prs[i])
		s.SourceRepository = c.Pulls.SourceName(&prs[i])
		s.Labels = labels(prLabels[prs[i].Id])
		resp.PullRequests = append(resp.PullRequests, s)
	}
	for s, n := range counts {
		resp.Counts[string(s)] = n
	}
	return resp, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Detail, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	pr, err := c.Pulls.Create(id, path, auth.UserID, application.NewPullRequest{Title: req.Title, Body: req.Body, Source: req.Source, Target: req.Target,
		Draft: req.Draft, SourceRepository: req.SourceRepository, AllowMaintainerEdits: req.AllowMaintainerEdits})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	view, err := c.Pulls.Get(id, path, auth.UserID, pr.Number)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}

func (c Controller) Get(ctx *fiber.Ctx, req NumberRequest) (*Detail, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	view, err := c.Pulls.Get(id, path, c.userId(ctx), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*Detail, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	view, err := c.Pulls.Update(id, path, auth.UserID, n, application.PullRequestUpdate{Title: req.Title, Body: req.Body, Draft: req.Draft, AllowMaintainerEdits: req.AllowMaintainerEdits})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}

func (c Controller) Close(ctx *fiber.Ctx, req NumberRequest) (*Detail, *fiberoapi.ErrorResponse) {
	return c.transition(ctx, req, c.Pulls.Close)
}

func (c Controller) Reopen(ctx *fiber.Ctx, req NumberRequest) (*Detail, *fiberoapi.ErrorResponse) {
	return c.transition(ctx, req, c.Pulls.Reopen)
}

func (c Controller) transition(ctx *fiber.Ctx, req NumberRequest, do func(id, path *string, actor string, number int) (*application.PullRequestView, error)) (*Detail, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	view, err := do(id, path, auth.UserID, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}

func (c Controller) Merge(ctx *fiber.Ctx, req MergeRequest) (*Detail, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	view, err := c.Pulls.Merge(id, path, auth.UserID, n, application.MergeOptions{Method: req.Method, Message: req.Message, DeleteBranch: req.DeleteBranch})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}

func (c Controller) Preview(ctx *fiber.Ctx, req PreviewRequest) (*gitbrowse.Comparison, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	cmp, err := c.Pulls.Preview(id, path, c.userId(ctx), req.SourceRepository, req.Source, req.Target)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return cmp, nil
}

func (c Controller) Compare(ctx *fiber.Ctx, req NumberRequest) (*gitbrowse.Comparison, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	cmp, err := c.Pulls.Compare(id, path, c.userId(ctx), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return cmp, nil
}
