package application

import (
	"fmt"
	"net/url"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
)

// Pull request, review and comment payloads, shaped like GitHub's.

type HookLabel struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"` // without "#", like GitHub
	Description string `json:"description"`
}

type HookBranch struct {
	Label string         `json:"label"` // owner:branch
	Ref   string         `json:"ref"`
	SHA   string         `json:"sha"`
	User  HookOwner      `json:"user"`
	Repo  HookRepository `json:"repo"`
}

type HookPullRequest struct {
	URL            string         `json:"url"`
	Id             string         `json:"id"`
	Number         int            `json:"number"`
	State          string         `json:"state"` // open or closed
	Locked         bool           `json:"locked"`
	Title          string         `json:"title"`
	User           *HookUser      `json:"user"` // null once the author's account is deleted
	Body           string         `json:"body"`
	Labels         []HookLabel    `json:"labels"`
	Draft          bool           `json:"draft"`
	HTMLURL        string         `json:"html_url"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	ClosedAt       *time.Time     `json:"closed_at"`
	MergedAt       *time.Time     `json:"merged_at"`
	Merged         bool           `json:"merged"`
	MergeCommitSHA *string        `json:"merge_commit_sha"`
	MergedBy       *HookUser      `json:"merged_by"`
	Assignee       *HookUser      `json:"assignee"`
	Assignees      []HookUser     `json:"assignees"`
	Milestone      *HookMilestone `json:"milestone"`
	Head           HookBranch     `json:"head"`
	Base           HookBranch     `json:"base"`
}

// PullRequestPayload: Assignee is set for assigned and unassigned,
// Milestone (top level) for milestoned and demilestoned.
type PullRequestPayload struct {
	Action      string          `json:"action"`
	Number      int             `json:"number"`
	PullRequest HookPullRequest `json:"pull_request"`
	Changes     map[string]any  `json:"changes,omitempty"`
	Label       *HookLabel      `json:"label,omitempty"`
	Assignee    *HookUser       `json:"assignee,omitempty"`
	Milestone   *HookMilestone  `json:"milestone,omitempty"`
	Before      string          `json:"before,omitempty"` // synchronize
	After       string          `json:"after,omitempty"`
	Repository  HookRepository  `json:"repository"`
	Sender      HookUser        `json:"sender"`
}

type HookReview struct {
	Id          string    `json:"id"`
	User        *HookUser `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"` // approved, changes_requested or commented
	HTMLURL     string    `json:"html_url"`
	CommitId    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type ReviewPayload struct {
	Action      string          `json:"action"`
	Review      HookReview      `json:"review"`
	PullRequest HookPullRequest `json:"pull_request"`
	Repository  HookRepository  `json:"repository"`
	Sender      HookUser        `json:"sender"`
}

type HookReviewComment struct {
	Id                  string    `json:"id"`
	PullRequestReviewId *string   `json:"pull_request_review_id"`
	User                *HookUser `json:"user"`
	Body                string    `json:"body"`
	Path                string    `json:"path"`
	Line                int       `json:"line"`
	Side                string    `json:"side"` // LEFT (old) or RIGHT (new)
	CommitId            string    `json:"commit_id"`
	HTMLURL             string    `json:"html_url"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type ReviewCommentPayload struct {
	Action      string            `json:"action"`
	Comment     HookReviewComment `json:"comment"`
	Changes     map[string]any    `json:"changes,omitempty"`
	PullRequest HookPullRequest   `json:"pull_request"`
	Repository  HookRepository    `json:"repository"`
	Sender      HookUser          `json:"sender"`
}

// HookIssue is a pull request seen as an issue, as in GitHub's
// issue_comment events (pull_request is set).
type HookIssue struct {
	URL         string           `json:"url"`
	Id          string           `json:"id"`
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	User        *HookUser        `json:"user"`
	Labels      []HookLabel      `json:"labels"`
	State       string           `json:"state"`
	Body        string           `json:"body"`
	HTMLURL     string           `json:"html_url"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	ClosedAt    *time.Time       `json:"closed_at"`
	Assignee    *HookUser        `json:"assignee"`
	Assignees   []HookUser       `json:"assignees"`
	Milestone   *HookMilestone   `json:"milestone"`
	PullRequest *HookIssuePullRq `json:"pull_request,omitempty"` // pull requests only
}

type HookIssuePullRq struct {
	URL      string     `json:"url"`
	HTMLURL  string     `json:"html_url"`
	MergedAt *time.Time `json:"merged_at"`
}

type HookIssueComment struct {
	Id        string    `json:"id"`
	User      *HookUser `json:"user"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type IssueCommentPayload struct {
	Action     string           `json:"action"`
	Issue      HookIssue        `json:"issue"`
	Comment    HookIssueComment `json:"comment"`
	Changes    map[string]any   `json:"changes,omitempty"`
	Repository HookRepository   `json:"repository"`
	Sender     HookUser         `json:"sender"`
}

func (c *WebhookApp) user(id *string) *HookUser {
	if id == nil || *id == "" {
		return nil
	}
	u := c.sender(*id, "")
	if u.Id == "" {
		return nil // deleted meanwhile
	}
	return &u
}

func hookLabels(labels []domain.Label) []HookLabel {
	out := make([]HookLabel, 0, len(labels))
	for _, l := range labels {
		out = append(out, hookLabel(l))
	}
	return out
}

func hookLabel(l domain.Label) HookLabel {
	color := l.Color
	if len(color) > 0 && color[0] == '#' {
		color = color[1:]
	}
	return HookLabel{Id: l.Id, Name: l.Name, Color: color, Description: l.Description}
}

func (c *WebhookApp) pullURLs(repo *domain.Repository, number int) (api, html string) {
	base := c.baseURL()
	return fmt.Sprintf("%s/api/v1/repository/%s/pulls/%d", base, url.PathEscape(repo.FullPath()), number),
		fmt.Sprintf("%s/%s/pulls/%d", base, repo.FullPath(), number)
}

// pullRequest describes pr. Its HeadCommit and BaseCommit are the branch
// tips (PullRequestApp.Get fills them in for open pull requests).
func (c *WebhookApp) pullRequest(repo *domain.Repository, pr *domain.PullRequest, labels []domain.Label) HookPullRequest {
	api, html := c.pullURLs(repo, pr.Number)
	r := c.repositoryPayload(repo)
	branch := func(name, sha string) HookBranch {
		return HookBranch{Label: repo.Namespace + ":" + name, Ref: name, SHA: sha, User: r.Owner, Repo: r}
	}
	p := HookPullRequest{
		URL: api, Id: pr.Id, Number: pr.Number, State: "open", Title: pr.Title, User: c.user(pr.AuthorId), Body: pr.Body,
		Labels: hookLabels(labels), Draft: pr.Draft, HTMLURL: html, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt,
		ClosedAt: pr.ClosedAt, MergedAt: pr.MergedAt, Merged: pr.State == domain.PullRequestMerged,
		Head: branch(pr.SourceBranch, pr.HeadCommit), Base: branch(pr.TargetBranch, pr.BaseCommit),
	}
	if pr.State != domain.PullRequestOpen {
		p.State = "closed"
	}
	p.Assignee, p.Assignees = c.assignees(domain.AssignPullRequest, pr.Id)
	p.Milestone = c.milestone(repo, pr.MilestoneId)
	if p.Merged {
		p.MergeCommitSHA, p.MergedBy = &pr.MergeCommit, c.user(pr.MergedById)
		if p.ClosedAt == nil {
			p.ClosedAt = pr.MergedAt
		}
	}
	return p
}

// PullRequestEvent queues a pull_request event.
func (c *WebhookApp) PullRequestEvent(view *PullRequestView, action, actorId string, extra PullRequestPayload) {
	if c == nil {
		return
	}
	p := extra
	p.Action, p.Number = action, view.Number
	p.PullRequest = c.pullRequest(view.Repository, view.PullRequest, view.Labels)
	p.Repository, p.Sender = c.repositoryPayload(view.Repository), c.sender(actorId, "")
	c.emit(view.Repository, "pull_request", action, p)
}

// ReviewSubmitted queues a pull_request_review event, and a
// pull_request_review_comment event for each of its inline comments.
func (c *WebhookApp) ReviewSubmitted(view *PullRequestView, review *domain.Review, comments []domain.Comment, actorId string) {
	if c == nil {
		return
	}
	_, html := c.pullURLs(view.Repository, view.Number)
	pr := c.pullRequest(view.Repository, view.PullRequest, view.Labels)
	c.emit(view.Repository, "pull_request_review", "submitted", ReviewPayload{
		Action: "submitted",
		Review: HookReview{Id: review.Id, User: c.user(review.AuthorId), Body: review.Body, State: string(review.State),
			HTMLURL: html + "#review-" + review.Id, CommitId: review.CommitId, SubmittedAt: review.CreatedAt},
		PullRequest: pr, Repository: c.repositoryPayload(view.Repository), Sender: c.sender(actorId, ""),
	})
	for i := range comments {
		c.CommentEvent(view, &comments[i], "created", actorId, nil)
	}
}

// CommentEvent queues a comment's created, edited or deleted event:
// pull_request_review_comment for inline comments, issue_comment for the
// conversation.
func (c *WebhookApp) CommentEvent(view *PullRequestView, comment *domain.Comment, action, actorId string, changes map[string]any) {
	if c == nil {
		return
	}
	api, html := c.pullURLs(view.Repository, view.Number)
	repo, sender := c.repositoryPayload(view.Repository), c.sender(actorId, "")
	if comment.Path != "" {
		side := "RIGHT"
		if comment.Side == "old" {
			side = "LEFT"
		}
		c.emit(view.Repository, "pull_request_review_comment", action, ReviewCommentPayload{
			Action: action, Changes: changes,
			Comment: HookReviewComment{Id: comment.Id, PullRequestReviewId: comment.ReviewId, User: c.user(comment.AuthorId), Body: comment.Body,
				Path: comment.Path, Line: comment.Line, Side: side, CommitId: comment.CommitId, HTMLURL: html + "#comment-" + comment.Id,
				CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt},
			PullRequest: c.pullRequest(view.Repository, view.PullRequest, view.Labels), Repository: repo, Sender: sender,
		})
		return
	}
	pr := view.PullRequest
	issue := HookIssue{URL: api, Id: pr.Id, Number: pr.Number, Title: pr.Title, User: c.user(pr.AuthorId), Labels: hookLabels(view.Labels),
		State: "open", Body: pr.Body, HTMLURL: html, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt, ClosedAt: pr.ClosedAt,
		PullRequest: &HookIssuePullRq{URL: api, HTMLURL: html, MergedAt: pr.MergedAt}}
	issue.Assignee, issue.Assignees = c.assignees(domain.AssignPullRequest, pr.Id)
	issue.Milestone = c.milestone(view.Repository, pr.MilestoneId)
	if pr.State != domain.PullRequestOpen {
		issue.State = "closed"
	}
	c.emit(view.Repository, "issue_comment", action, IssueCommentPayload{
		Action: action, Issue: issue, Changes: changes,
		Comment: HookIssueComment{Id: comment.Id, User: c.user(comment.AuthorId), Body: comment.Body, HTMLURL: html + "#comment-" + comment.Id,
			CreatedAt: comment.CreatedAt, UpdatedAt: comment.UpdatedAt},
		Repository: repo, Sender: sender,
	})
}

// synchronize queues pull_request synchronize events for the open pull
// requests whose source branch a push moved.
func (c *WebhookApp) synchronize(repo *domain.Repository, pusher Pusher, refs storer.ReferenceStorer, updates []RefUpdate) {
	if c.Pulls == nil {
		return
	}
	moved := map[string]RefUpdate{}
	for _, u := range updates {
		if u.Name.IsBranch() && !u.Old.IsZero() && !u.New.IsZero() {
			moved[u.Name.Short()] = u
		}
	}
	if len(moved) == 0 {
		return
	}
	open, _, err := c.Pulls.List(repo.Id, domain.PullRequestOpen, "", 0, 1000)
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.webhook").Msg("failed to list pull requests")
		return
	}
	for i := range open {
		pr := &open[i]
		u, ok := moved[pr.SourceBranch]
		if !ok {
			continue
		}
		pr.HeadCommit = u.New.String()
		if target, err := refs.Reference(plumbing.NewBranchReferenceName(pr.TargetBranch)); err == nil {
			pr.BaseCommit = target.Hash().String()
		}
		var labels []domain.Label
		if c.Labels != nil {
			if byPR, err := c.Labels.For([]string{pr.Id}); err == nil {
				labels = byPR[pr.Id]
			}
		}
		c.emit(repo, "pull_request", "synchronize", PullRequestPayload{
			Action: "synchronize", Number: pr.Number, PullRequest: c.pullRequest(repo, pr, labels),
			Before: u.Old.String(), After: u.New.String(),
			Repository: c.repositoryPayload(repo), Sender: c.sender(pusher.UserId, pusher.Name),
		})
	}
}
