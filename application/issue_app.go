package application

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/rs/zerolog"
)

// Issue rules:
//   - issues and pull requests share one number sequence per repository
//     (#12 is one or the other; see Resolve);
//   - anyone who can read the repository reads its issues (anonymous on
//     public ones); signed-in readers open issues and comment;
//   - the author and developers and up edit an issue's title and body, and
//     close or reopen it; maintainers and up delete issues;
//   - comment authors edit their comments; they and maintainers delete them;
//   - developers and up set labels (the repository's, shared with pull
//     requests);
//   - owners turn issues off per repository: existing issues stay readable
//     and nothing can be written; archived repositories are read-only too;
//   - developers and up set assignees and the milestone (milestone_app.go);
//   - "Fixes #N" (close, closes, closed, fix, fixes, fixed, resolve,
//     resolves, resolved) in a pull request's title or body closes issue #N
//     when the pull request is merged into the default branch;
//   - participants (author, assignees, commenters, @mentioned) are emailed
//     about others' activity when SMTP is configured, if they can read the
//     repository.

var (
	ErrIssueNotFound  = errors.New("issue not found")
	ErrIssuesDisabled = errors.New("issues are turned off for this repository")
	ErrNumberNotFound = errors.New("no issue or pull request has this number")
	ErrInvalidState   = errors.New("state must be open, closed or all")
)

type IssueApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Issues       domain.IssuePers
	Pulls        domain.PullRequestPers
	Labels       domain.LabelPers
	Assignees    domain.AssigneePers
	Milestones   domain.MilestonePers
	Webhooks     *WebhookApp
	Mailer       mail.Sender // nil or without smtp.host: no emails
}

// IssueView is an issue with what the viewer may do.
type IssueView struct {
	*domain.Issue
	Repository *domain.Repository
	Role       domain.AccessType
	Labels     []domain.Label
	Assignees  []domain.User
	Milestone  *domain.Milestone
	// CanEdit: change the title and body, close and reopen.
	CanEdit bool
	// CanComment: signed in, and the repository takes writes.
	CanComment bool
	// CanLabel / CanDelete: developers / maintainers and up.
	CanLabel  bool
	CanDelete bool
	viewerId  string
}

func (c *IssueApp) repo(id, path *string, userId string) (*domain.Repository, domain.AccessType, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, domain.AccessTypeNone, err
	}
	role := domain.AccessTypeNone
	if userId != "" {
		if role, err = c.Repositories.GetUserEffectiveRole(repo.Id, userId); err != nil {
			return nil, domain.AccessTypeNone, err
		}
	}
	return repo, role, nil
}

// writable is nil if the repository takes issue writes.
func writable(repo *domain.Repository) error {
	switch {
	case !repo.HasIssues:
		return ErrIssuesDisabled
	case repo.IsArchived:
		return ErrGitArchived
	}
	return nil
}

func (c *IssueApp) Create(id, path *string, actorId, title, body string) (*IssueView, error) {
	if actorId == "" {
		return nil, ErrForbidden
	}
	repo, _, err := c.repo(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if err := writable(repo); err != nil {
		return nil, err
	}
	if title = strings.TrimSpace(title); title == "" {
		return nil, ErrEmptyTitle
	}
	issue := &domain.Issue{RepositoryId: repo.Id, Title: title, Body: body, AuthorId: &actorId, State: domain.IssueOpen}
	if err := c.Issues.Create(issue); err != nil {
		return nil, err
	}
	v, err := c.Get(&repo.Id, nil, actorId, issue.Number)
	if err != nil {
		return nil, err
	}
	c.Webhooks.IssuesEvent(v, "opened", actorId, IssuesPayload{})
	if len(Mentions(body)) > 0 {
		c.notify(v, actorId, "opened", body, nil)
	}
	return v, nil
}

// IssueList is a page of issues with their labels, assignees and
// milestones, and counts per state.
type IssueList struct {
	Issues     []domain.Issue
	Labels     map[string][]domain.Label
	Assignees  map[string][]domain.User
	Milestones map[string]domain.Milestone
	Counts     map[domain.IssueState]int64
}

// IssueQuery filters List: label, author and assignee are names
// (assignee "none": unassigned), milestone an id ("none": without one);
// mine is "assigned" or "created" (the caller's).
type IssueQuery struct {
	State     string // open (default), closed or all
	Label     string
	Author    string
	Assignee  string
	Milestone string
	Mine      string
	Query     string
}

func (c *IssueApp) List(id, path *string, userId string, q IssueQuery, offset, limit int) (*IssueList, error) {
	repo, _, err := c.repo(id, path, userId)
	if err != nil {
		return nil, err
	}
	f := domain.IssueFilter{Query: strings.TrimSpace(q.Query)}
	switch q.State {
	case "", "open":
		f.State = domain.IssueOpen
	case "closed":
		f.State = domain.IssueClosed
	case "all":
	default:
		return nil, ErrInvalidState
	}
	empty := &IssueList{Issues: []domain.Issue{}, Labels: map[string][]domain.Label{}, Assignees: map[string][]domain.User{},
		Milestones: map[string]domain.Milestone{}, Counts: map[domain.IssueState]int64{}}
	if q.Label != "" {
		labels, err := c.Labels.List(repo.Id)
		if err != nil {
			return nil, err
		}
		for _, l := range labels {
			if l.Name == q.Label {
				f.LabelId = l.Id
			}
		}
		if f.LabelId == "" {
			return empty, nil // no such label: nothing has it
		}
	}
	if q.Author != "" {
		u, err := c.Repositories.UserPers.GetByUsername(q.Author)
		if err != nil {
			return empty, nil
		}
		f.AuthorId = u.Id
	}
	switch q.Assignee {
	case "", domain.NoneFilter:
		f.AssigneeId = q.Assignee
	default:
		u, err := c.Repositories.UserPers.GetByUsername(q.Assignee)
		if err != nil {
			return empty, nil
		}
		f.AssigneeId = u.Id
	}
	switch q.Milestone {
	case "", domain.NoneFilter:
		f.MilestoneId = q.Milestone
	default:
		if _, err := c.Milestones.Get(repo.Id, q.Milestone); err != nil {
			return empty, nil // not this repository's (or not a UUID on Postgres)
		}
		f.MilestoneId = q.Milestone
	}
	switch q.Mine {
	case "":
	case "assigned", "created":
		if !domain.IsPerson(userId) {
			return nil, ErrForbidden
		}
		if q.Mine == "assigned" {
			f.AssigneeId = userId
		} else {
			f.AuthorId = userId
		}
	default:
		return nil, ErrInvalidState
	}
	issues, counts, err := c.Issues.List(repo.Id, f, max(offset, 0), clampLimit(limit))
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(issues))
	for i, is := range issues {
		ids[i] = is.Id
	}
	labels, err := c.Issues.Labels(ids)
	if err != nil {
		return nil, err
	}
	assignees, err := c.Assignees.For(domain.AssignIssue, ids)
	if err != nil {
		return nil, err
	}
	milestones := map[string]domain.Milestone{}
	if ms, err := c.Milestones.List(repo.Id, ""); err == nil {
		for _, m := range ms {
			milestones[m.Id] = m
		}
	}
	return &IssueList{Issues: issues, Labels: labels, Assignees: assignees, Milestones: milestones, Counts: counts}, nil
}

func (c *IssueApp) Get(id, path *string, userId string, number int) (*IssueView, error) {
	repo, role, err := c.repo(id, path, userId)
	if err != nil {
		return nil, err
	}
	issue, err := c.Issues.Get(repo.Id, number)
	if isNotFound(err) {
		return nil, ErrIssueNotFound
	} else if err != nil {
		return nil, err
	}
	labels, err := c.Issues.Labels([]string{issue.Id})
	if err != nil {
		return nil, err
	}
	v := &IssueView{Issue: issue, Repository: repo, Role: role, Labels: labels[issue.Id], viewerId: userId}
	if v.Labels == nil {
		v.Labels = []domain.Label{}
	}
	assignees, err := c.Assignees.For(domain.AssignIssue, []string{issue.Id})
	if err != nil {
		return nil, err
	}
	v.Assignees = assignees[issue.Id]
	if issue.MilestoneId != nil {
		if m, err := c.Milestones.Get(repo.Id, *issue.MilestoneId); err == nil {
			v.Milestone = m
		}
	}
	open := writable(repo) == nil && domain.IsPerson(userId)
	isAuthor := domain.IsPerson(userId) && issue.AuthorId != nil && *issue.AuthorId == userId
	v.CanEdit = open && (isAuthor || role.CanWrite())
	v.CanComment = open
	v.CanLabel = open && role.CanWrite()
	v.CanDelete = open && role.CanAdmin()
	return v, nil
}

// Viewer is who the view was computed for ("" anonymous).
func (v *IssueView) Viewer() string { return v.viewerId }

// editable returns the issue if actorId may change it with allowed.
func (c *IssueApp) editable(id, path *string, actorId string, number int, allowed func(*IssueView) bool) (*IssueView, error) {
	if actorId == "" {
		return nil, ErrForbidden
	}
	v, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if err := writable(v.Repository); err != nil {
		return nil, err
	}
	if !allowed(v) {
		return nil, ErrForbidden
	}
	return v, nil
}

type IssueUpdate struct {
	Title *string
	Body  *string
	State *string // open or closed
}

func (c *IssueApp) Update(id, path *string, actorId string, number int, upd IssueUpdate) (*IssueView, error) {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanEdit })
	if err != nil {
		return nil, err
	}
	now := time.Now()
	fields := map[string]any{"updated_at": now}
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
	if upd.State != nil && domain.IssueState(*upd.State) != v.State {
		switch domain.IssueState(*upd.State) {
		case domain.IssueClosed:
			fields["state"], fields["closed_at"], fields["closed_by_id"] = domain.IssueClosed, now, actorId
		case domain.IssueOpen:
			fields["state"], fields["closed_at"], fields["closed_by_id"] = domain.IssueOpen, nil, nil
		default:
			return nil, ErrInvalidState
		}
	}
	if err := c.Issues.Update(v.Id, fields); err != nil {
		return nil, err
	}
	nv, err := c.Get(&v.RepositoryId, nil, actorId, number)
	if err != nil {
		return nil, err
	}
	changes := map[string]any{}
	if nv.Title != v.Title {
		changes["title"] = map[string]string{"from": v.Title}
	}
	if nv.Body != v.Body {
		changes["body"] = map[string]string{"from": v.Body}
	}
	if len(changes) > 0 {
		c.Webhooks.IssuesEvent(nv, "edited", actorId, IssuesPayload{Changes: changes})
	}
	if nv.State != v.State {
		action := map[domain.IssueState]string{domain.IssueClosed: "closed", domain.IssueOpen: "reopened"}[nv.State]
		c.Webhooks.IssuesEvent(nv, action, actorId, IssuesPayload{})
		c.notify(nv, actorId, action, "", nil)
	}
	return nv, nil
}

func (c *IssueApp) Delete(id, path *string, actorId string, number int) error {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanDelete })
	if err != nil {
		return err
	}
	if err := c.Issues.Delete(v.Id); err != nil {
		return err
	}
	c.Webhooks.IssuesEvent(v, "deleted", actorId, IssuesPayload{})
	return nil
}

// Comments returns an issue and its comments, oldest first.
func (c *IssueApp) Comments(id, path *string, userId string, number int) (*IssueView, []domain.IssueComment, error) {
	v, err := c.Get(id, path, userId, number)
	if err != nil {
		return nil, nil, err
	}
	comments, err := c.Issues.Comments(v.Id)
	return v, comments, err
}

func (c *IssueApp) AddComment(id, path *string, actorId string, number int, body string) (*domain.IssueComment, error) {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanComment })
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, ErrEmptyComment
	}
	comment := &domain.IssueComment{IssueId: v.Id, AuthorId: &actorId, Body: body}
	if err := c.Issues.CreateComment(comment); err != nil {
		return nil, err
	}
	_ = c.Issues.Update(v.Id, map[string]any{"updated_at": time.Now()})
	c.Webhooks.IssueCommentEvent(v, comment, "created", actorId, nil)
	c.notify(v, actorId, "commented on", body, nil)
	return comment, nil
}

func (c *IssueApp) comment(v *IssueView, commentId string) (*domain.IssueComment, error) {
	comment, err := c.Issues.Comment(v.Id, commentId)
	if isNotFound(err) {
		return nil, ErrCommentNotFound
	}
	return comment, err
}

func (c *IssueApp) EditComment(id, path *string, actorId string, number int, commentId, body string) error {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanComment })
	if err != nil {
		return err
	}
	comment, err := c.comment(v, commentId)
	if err != nil {
		return err
	}
	if comment.AuthorId == nil || *comment.AuthorId != actorId {
		return ErrForbidden
	}
	if strings.TrimSpace(body) == "" {
		return ErrEmptyComment
	}
	if err := c.Issues.UpdateComment(commentId, body); err != nil {
		return err
	}
	changes := map[string]any{"body": map[string]string{"from": comment.Body}}
	comment.Body, comment.UpdatedAt = body, time.Now()
	c.Webhooks.IssueCommentEvent(v, comment, "edited", actorId, changes)
	return nil
}

func (c *IssueApp) DeleteComment(id, path *string, actorId string, number int, commentId string) error {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanComment })
	if err != nil {
		return err
	}
	comment, err := c.comment(v, commentId)
	if err != nil {
		return err
	}
	if (comment.AuthorId == nil || *comment.AuthorId != actorId) && !v.Role.CanAdmin() {
		return ErrForbidden
	}
	if err := c.Issues.DeleteComment(commentId); err != nil {
		return err
	}
	c.Webhooks.IssueCommentEvent(v, comment, "deleted", actorId, nil)
	return nil
}

// SetLabels replaces an issue's labels by name.
func (c *IssueApp) SetLabels(id, path *string, actorId string, number int, names []string) ([]domain.Label, error) {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanLabel })
	if err != nil {
		return nil, err
	}
	all, err := c.Labels.List(v.RepositoryId)
	if err != nil {
		return nil, err
	}
	byName := map[string]domain.Label{}
	for _, l := range all {
		byName[l.Name] = l
	}
	ids, set := []string{}, []domain.Label{}
	for _, n := range names {
		l, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrLabelNotFound, n)
		}
		ids, set = append(ids, l.Id), append(set, l)
	}
	if err := c.Issues.SetLabels(v.Id, ids); err != nil {
		return nil, err
	}
	before := map[string]bool{}
	for _, l := range v.Labels {
		before[l.Id] = true
	}
	after := map[string]bool{}
	for _, l := range set {
		after[l.Id] = true
	}
	old := v.Labels
	v.Labels = set
	for _, l := range set {
		if !before[l.Id] {
			label := hookLabel(l)
			c.Webhooks.IssuesEvent(v, "labeled", actorId, IssuesPayload{Label: &label})
		}
	}
	for _, l := range old {
		if !after[l.Id] {
			label := hookLabel(l)
			c.Webhooks.IssuesEvent(v, "unlabeled", actorId, IssuesPayload{Label: &label})
		}
	}
	return set, nil
}

// Resolve says whether number is an issue or a pull request ("issue" or
// "pull"), for links such as #12.
func (c *IssueApp) Resolve(id, path *string, userId string, number int) (string, error) {
	repo, _, err := c.repo(id, path, userId)
	if err != nil {
		return "", err
	}
	if _, err := c.Issues.Get(repo.Id, number); err == nil {
		return "issue", nil
	} else if !isNotFound(err) {
		return "", err
	}
	if _, err := c.Pulls.Get(repo.Id, number); err == nil {
		return "pull", nil
	} else if !isNotFound(err) {
		return "", err
	}
	return "", ErrNumberNotFound
}

// SetAssignees replaces an issue's assignees by username.
func (c *IssueApp) SetAssignees(id, path *string, actorId string, number int, names []string) ([]domain.User, error) {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanLabel })
	if err != nil {
		return nil, err
	}
	users, err := c.Repositories.assignees(v.Repository, names)
	if err != nil {
		return nil, err
	}
	if err := c.Assignees.Set(domain.AssignIssue, v.Id, userIds(users)); err != nil {
		return nil, err
	}
	added, removed := assignmentDiff(v.Assignees, users)
	v.Assignees = users
	for _, u := range added {
		hu := c.Webhooks.hookUserOrNil(u)
		c.Webhooks.IssuesEvent(v, "assigned", actorId, IssuesPayload{Assignee: hu})
	}
	for _, u := range removed {
		hu := c.Webhooks.hookUserOrNil(u)
		c.Webhooks.IssuesEvent(v, "unassigned", actorId, IssuesPayload{Assignee: hu})
	}
	if len(added) > 0 {
		c.notify(v, actorId, "assigned "+usernames(added)+" to", "", added)
	}
	if users == nil {
		users = []domain.User{}
	}
	return users, nil
}

func usernames(users []domain.User) string {
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Username
	}
	return strings.Join(names, ", ")
}

// SetMilestone sets an issue's milestone ("" removes it).
func (c *IssueApp) SetMilestone(id, path *string, actorId string, number int, milestoneId string) (*IssueView, error) {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanLabel })
	if err != nil {
		return nil, err
	}
	var value any
	if milestoneId != "" {
		m, err := c.Milestones.Get(v.RepositoryId, milestoneId)
		if err != nil {
			return nil, ErrMilestoneNotFound
		}
		value = m.Id
	}
	if err := c.Issues.Update(v.Id, map[string]any{"milestone_id": value, "updated_at": time.Now()}); err != nil {
		return nil, err
	}
	nv, err := c.Get(&v.RepositoryId, nil, actorId, number)
	if err != nil {
		return nil, err
	}
	old := v.MilestoneId
	switch {
	case nv.MilestoneId != nil && (old == nil || *old != *nv.MilestoneId):
		c.Webhooks.IssuesEvent(nv, "milestoned", actorId, IssuesPayload{Milestone: c.Webhooks.milestone(nv.Repository, nv.MilestoneId)})
	case nv.MilestoneId == nil && old != nil:
		c.Webhooks.IssuesEvent(nv, "demilestoned", actorId, IssuesPayload{Milestone: c.Webhooks.milestone(nv.Repository, old)})
	}
	return nv, nil
}

var closingPattern = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?\s+#(\d+)\b`)

// ClosingReferences returns the issue numbers text closes ("Fixes #12").
func ClosingReferences(text string) []int {
	var out []int
	seen := map[int]bool{}
	for _, m := range closingPattern.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// CloseFromPull closes the open issues a merged pull request references
// ("Fixes #N"), as actorId, the merger. Errors are logged: the merge
// happened.
func (c *IssueApp) CloseFromPull(repo *domain.Repository, pr *domain.PullRequest, actorId string) {
	if c == nil {
		return
	}
	for _, n := range ClosingReferences(pr.Title + "\n" + pr.Body) {
		issue, err := c.Issues.Get(repo.Id, n)
		if err != nil || issue.State != domain.IssueOpen {
			continue // a pull request, unknown, or already closed
		}
		now, prNumber := time.Now(), pr.Number
		if err := c.Issues.Update(issue.Id, map[string]any{"state": domain.IssueClosed, "closed_at": now, "closed_by_id": actorId,
			"closed_by_pull": prNumber, "updated_at": now}); err != nil {
			c.Logger.Error().Err(err).Str("component", "application.issue").Int("issue", n).Msg("failed to close issue from pull request")
			continue
		}
		v, err := c.Get(&repo.Id, nil, actorId, n)
		if err != nil {
			continue
		}
		c.Webhooks.IssuesEvent(v, "closed", actorId, IssuesPayload{})
		c.notify(v, actorId, fmt.Sprintf("closed (with pull request #%d)", prNumber), "", nil)
	}
}
