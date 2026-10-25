package application

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
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
//     and nothing can be written; archived repositories are read-only too.

var (
	ErrIssueNotFound  = errors.New("issue not found")
	ErrIssuesDisabled = errors.New("issues are turned off for this repository")
	ErrNumberNotFound = errors.New("no issue or pull request has this number")
	ErrInvalidState   = errors.New("state must be open, closed or all")
)

type IssueApp struct {
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Issues       domain.IssuePers
	Pulls        domain.PullRequestPers
	Labels       domain.LabelPers
}

// IssueView is an issue with what the viewer may do.
type IssueView struct {
	*domain.Issue
	Repository *domain.Repository
	Role       domain.AccessType
	Labels     []domain.Label
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
	return c.Get(&repo.Id, nil, actorId, issue.Number)
}

// IssueList is a page of issues with their labels and counts per state.
type IssueList struct {
	Issues []domain.Issue
	Labels map[string][]domain.Label
	Counts map[domain.IssueState]int64
}

// IssueQuery filters List: label and author are names.
type IssueQuery struct {
	State  string // open (default), closed or all
	Label  string
	Author string
	Query  string
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
	empty := &IssueList{Issues: []domain.Issue{}, Labels: map[string][]domain.Label{}, Counts: map[domain.IssueState]int64{}}
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
	return &IssueList{Issues: issues, Labels: labels, Counts: counts}, nil
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
	open := writable(repo) == nil && userId != ""
	isAuthor := userId != "" && issue.AuthorId != nil && *issue.AuthorId == userId
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
	return c.Get(&v.RepositoryId, nil, actorId, number)
}

func (c *IssueApp) Delete(id, path *string, actorId string, number int) error {
	v, err := c.editable(id, path, actorId, number, func(v *IssueView) bool { return v.CanDelete })
	if err != nil {
		return err
	}
	return c.Issues.Delete(v.Id)
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
	return c.Issues.UpdateComment(commentId, body)
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
	return c.Issues.DeleteComment(commentId)
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
