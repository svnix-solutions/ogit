package application

import (
	"fmt"
	"net/url"
	"time"

	"github.com/labbs/ogit/domain"
)

// GitHub's "issues" and "milestone" events, and issue comments as
// "issue_comment" (an issue without pull_request).

type HookMilestone struct {
	URL          string     `json:"url"`
	HTMLURL      string     `json:"html_url"`
	Id           string     `json:"id"`
	Title        string     `json:"title"`
	Description  string     `json:"description"`
	State        string     `json:"state"`
	OpenIssues   int64      `json:"open_issues"`
	ClosedIssues int64      `json:"closed_issues"`
	DueOn        *time.Time `json:"due_on"`
	ClosedAt     *time.Time `json:"closed_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// IssuesPayload: Label for labeled/unlabeled, Assignee for
// assigned/unassigned, Milestone for milestoned/demilestoned.
type IssuesPayload struct {
	Action     string         `json:"action"`
	Issue      HookIssue      `json:"issue"`
	Changes    map[string]any `json:"changes,omitempty"`
	Label      *HookLabel     `json:"label,omitempty"`
	Assignee   *HookUser      `json:"assignee,omitempty"`
	Milestone  *HookMilestone `json:"milestone,omitempty"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

type MilestonePayload struct {
	Action     string         `json:"action"`
	Milestone  HookMilestone  `json:"milestone"`
	Changes    map[string]any `json:"changes,omitempty"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

func (c *WebhookApp) hookMilestone(repo *domain.Repository, m *domain.Milestone, p domain.MilestoneProgress) *HookMilestone {
	base := c.baseURL()
	return &HookMilestone{
		URL: fmt.Sprintf("%s/api/v1/repository/%s/milestones/%s", base, url.PathEscape(repo.FullPath()), m.Id), HTMLURL: base + "/" + repo.FullPath() + "/milestones/" + m.Id,
		Id: m.Id, Title: m.Title, Description: m.Description, State: string(m.State), OpenIssues: p.Open, ClosedIssues: p.Closed,
		DueOn: m.DueOn, ClosedAt: m.ClosedAt, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// milestone describes milestone id of repo, or nil.
func (c *WebhookApp) milestone(repo *domain.Repository, id *string) *HookMilestone {
	if id == nil || c.Milestones == nil {
		return nil
	}
	m, err := c.Milestones.Get(repo.Id, *id)
	if err != nil {
		return nil
	}
	p, _ := c.Milestones.Progress([]string{m.Id})
	return c.hookMilestone(repo, m, p[m.Id])
}

// assignees returns the first assignee and all of them, as GitHub does.
func (c *WebhookApp) assignees(kind, itemId string) (*HookUser, []HookUser) {
	out := []HookUser{}
	if c.Assignees != nil {
		if byItem, err := c.Assignees.For(kind, []string{itemId}); err == nil {
			for _, u := range byItem[itemId] {
				out = append(out, c.hookUser(u))
			}
		}
	}
	if len(out) == 0 {
		return nil, out
	}
	return &out[0], out
}

func (c *WebhookApp) hookUser(u domain.User) HookUser {
	return HookUser{Login: u.Username, Id: u.Id, Type: "User", HTMLURL: c.baseURL() + "/" + u.Username, AvatarURL: u.AvatarUrl}
}

func (c *WebhookApp) issue(repo *domain.Repository, is *domain.Issue, labels []domain.Label) HookIssue {
	base := c.baseURL()
	h := HookIssue{
		URL: fmt.Sprintf("%s/api/v1/repository/%s/issues/%d", base, url.PathEscape(repo.FullPath()), is.Number), Id: is.Id, Number: is.Number,
		Title: is.Title, User: c.user(is.AuthorId), Labels: hookLabels(labels), State: string(is.State), Body: is.Body,
		HTMLURL: fmt.Sprintf("%s/%s/issues/%d", base, repo.FullPath(), is.Number), CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt, ClosedAt: is.ClosedAt,
	}
	h.Assignee, h.Assignees = c.assignees(domain.AssignIssue, is.Id)
	h.Milestone = c.milestone(repo, is.MilestoneId)
	return h
}

// IssuesEvent queues an "issues" event; extra carries changes, label,
// assignee or milestone.
func (c *WebhookApp) IssuesEvent(v *IssueView, action, actorId string, extra IssuesPayload) {
	if c == nil {
		return
	}
	p := extra
	p.Action, p.Issue = action, c.issue(v.Repository, v.Issue, v.Labels)
	p.Repository, p.Sender = c.repositoryPayload(v.Repository), c.sender(actorId, "")
	c.emit(v.Repository, "issues", action, p)
}

// IssueCommentEvent queues an "issue_comment" event for an issue comment.
func (c *WebhookApp) IssueCommentEvent(v *IssueView, cm *domain.IssueComment, action, actorId string, changes map[string]any) {
	if c == nil {
		return
	}
	c.emit(v.Repository, "issue_comment", action, IssueCommentPayload{
		Action: action, Issue: c.issue(v.Repository, v.Issue, v.Labels), Changes: changes,
		Comment: HookIssueComment{Id: cm.Id, User: c.user(cm.AuthorId), Body: cm.Body,
			HTMLURL: fmt.Sprintf("%s/%s/issues/%d#comment-%s", c.baseURL(), v.Repository.FullPath(), v.Number, cm.Id), CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt},
		Repository: c.repositoryPayload(v.Repository), Sender: c.sender(actorId, ""),
	})
}

// MilestoneEvent queues a "milestone" event (created, edited, closed,
// opened, deleted).
func (c *WebhookApp) MilestoneEvent(repo *domain.Repository, m *domain.Milestone, p domain.MilestoneProgress, action, actorId string, changes map[string]any) {
	if c == nil {
		return
	}
	c.emit(repo, "milestone", action, MilestonePayload{Action: action, Milestone: *c.hookMilestone(repo, m, p), Changes: changes,
		Repository: c.repositoryPayload(repo), Sender: c.sender(actorId, "")})
}

func (c *WebhookApp) hookUserOrNil(u domain.User) *HookUser {
	if c == nil {
		return nil
	}
	h := c.hookUser(u)
	return &h
}
