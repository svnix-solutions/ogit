package application

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Review rules:
//   - anyone who can read the repository comments and reviews (signed in);
//     not on archived repositories;
//   - authors only comment on their own pull requests (no approve or
//     request changes);
//   - each reviewer's latest approve or request-changes counts, if they are
//     a developer or above when the pull request is merged. Merging needs
//     at least the repository's required_approvals and no outstanding
//     request for changes;
//   - comment authors edit their comments; they and maintainers delete
//     them;
//   - maintainers manage labels; developers set them on pull requests;
//   - requested reviewers: pull_request_reviewers.go;
//   - participants (author, reviewers, commenters, @mentioned) are emailed
//     about others' activity when SMTP is configured.

var (
	ErrOwnReview        = errors.New("you can't approve or request changes on your own pull request")
	ErrEmptyComment     = errors.New("a comment can't be empty")
	ErrInvalidLine      = errors.New("an inline comment needs a path, a line and a side (old or new)")
	ErrCommentNotFound  = errors.New("comment not found")
	ErrInvalidReview    = errors.New("review state must be approved, changes_requested or commented")
	ErrNeedsApproval    = errors.New("not enough approvals")
	ErrChangesRequested = errors.New("changes were requested")
	ErrLabelNotFound    = errors.New("label not found")
	ErrInvalidLabel     = errors.New("labels need a name (1-50 characters) and a color like #1f883d")
	ErrLabelExists      = errors.New("a label with this name already exists")
	ErrInvalidApprovals = errors.New("required approvals must be between 0 and 10")
)

// Approvals is who approved and who requested changes, counting only
// reviewers who are developers or above and not the author.
type Approvals struct {
	Approved         []string
	ChangesRequested []string
}

// approvals counts the reviews; with headOnly, approvals of other commits
// are dismissed (requests for changes stay).
func (c *PullRequestApp) approvals(repo *domain.Repository, pr *domain.PullRequest, headOnly string) (*Approvals, error) {
	reviews, err := c.Reviews.Reviews(pr.Id)
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.Review{}
	for _, r := range reviews { // oldest first
		if r.AuthorId == nil || r.State == domain.ReviewCommented || (pr.AuthorId != nil && *r.AuthorId == *pr.AuthorId) {
			continue
		}
		if headOnly != "" && r.State == domain.ReviewApproved && r.CommitId != headOnly {
			delete(latest, *r.AuthorId) // dismissed by a newer push
			continue
		}
		latest[*r.AuthorId] = r
	}
	out := &Approvals{Approved: []string{}, ChangesRequested: []string{}}
	for userId, r := range latest {
		role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
		if err != nil {
			return nil, err
		}
		if !role.CanWrite() {
			continue
		}
		if r.State == domain.ReviewApproved {
			out.Approved = append(out.Approved, r.Author.Username)
		} else {
			out.ChangesRequested = append(out.ChangesRequested, r.Author.Username)
		}
	}
	return out, nil
}

// InlineTarget places a comment on a line of the diff.
type InlineTarget struct {
	Path string
	Line int
	Side string // old or new
}

func (t *InlineTarget) valid() bool {
	return t.Path != "" && t.Line > 0 && (t.Side == "old" || t.Side == "new")
}

// commentable returns the pull request if actorId may comment on it.
func (c *PullRequestApp) commentable(id, path *string, actorId string, number int) (*PullRequestView, error) {
	if actorId == "" {
		return nil, ErrForbidden
	}
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if view.Repository.IsArchived {
		return nil, ErrGitArchived
	}
	return view, nil
}

func (c *PullRequestApp) AddComment(id, path *string, actorId string, number int, body string, inline *InlineTarget) (*domain.Comment, error) {
	view, err := c.commentable(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, ErrEmptyComment
	}
	comment := &domain.Comment{PullRequestId: view.Id, AuthorId: &actorId, Body: body, CommitId: view.HeadCommit}
	if inline != nil {
		if !inline.valid() {
			return nil, ErrInvalidLine
		}
		comment.Path, comment.Line, comment.Side = inline.Path, inline.Line, inline.Side
	}
	if err := c.Reviews.CreateComment(comment); err != nil {
		return nil, err
	}
	c.notify(view, actorId, "commented", body)
	c.Webhooks.CommentEvent(view, comment, "created", actorId, nil)
	return comment, nil
}

func (c *PullRequestApp) EditComment(id, path *string, actorId string, number int, commentId, body string) error {
	view, err := c.commentable(id, path, actorId, number)
	if err != nil {
		return err
	}
	comment, err := c.Reviews.Comment(view.Id, commentId)
	if isNotFound(err) {
		return ErrCommentNotFound
	} else if err != nil {
		return err
	}
	if comment.AuthorId == nil || *comment.AuthorId != actorId {
		return ErrForbidden
	}
	if strings.TrimSpace(body) == "" {
		return ErrEmptyComment
	}
	if err := c.Reviews.UpdateComment(commentId, body); err != nil {
		return err
	}
	changes := map[string]any{"body": map[string]string{"from": comment.Body}}
	comment.Body, comment.UpdatedAt = body, time.Now()
	c.Webhooks.CommentEvent(view, comment, "edited", actorId, changes)
	return nil
}

func (c *PullRequestApp) DeleteComment(id, path *string, actorId string, number int, commentId string) error {
	view, err := c.commentable(id, path, actorId, number)
	if err != nil {
		return err
	}
	comment, err := c.Reviews.Comment(view.Id, commentId)
	if isNotFound(err) {
		return ErrCommentNotFound
	} else if err != nil {
		return err
	}
	if (comment.AuthorId == nil || *comment.AuthorId != actorId) && !view.Role.CanAdmin() {
		return ErrForbidden
	}
	if err := c.Reviews.DeleteComment(commentId); err != nil {
		return err
	}
	c.Webhooks.CommentEvent(view, comment, "deleted", actorId, nil)
	return nil
}

// SubmitReview records a review with optional inline comments.
func (c *PullRequestApp) SubmitReview(id, path *string, actorId string, number int, state domain.ReviewState, body string, inline []InlineComment) (*domain.Review, error) {
	view, err := c.commentable(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	switch state {
	case domain.ReviewApproved, domain.ReviewChangesRequested:
		if view.AuthorId != nil && *view.AuthorId == actorId {
			return nil, ErrOwnReview
		}
	case domain.ReviewCommented:
		if strings.TrimSpace(body) == "" && len(inline) == 0 {
			return nil, ErrEmptyComment
		}
	default:
		return nil, ErrInvalidReview
	}
	if view.State != domain.PullRequestOpen {
		return nil, ErrNotOpen
	}
	comments := make([]domain.Comment, 0, len(inline))
	for _, ic := range inline {
		if !ic.InlineTarget.valid() {
			return nil, ErrInvalidLine
		}
		if strings.TrimSpace(ic.Body) == "" {
			return nil, ErrEmptyComment
		}
		comments = append(comments, domain.Comment{AuthorId: &actorId, Body: ic.Body, Path: ic.Path, Line: ic.Line, Side: ic.Side, CommitId: view.HeadCommit})
	}
	review := &domain.Review{PullRequestId: view.Id, AuthorId: &actorId, State: state, Body: body, CommitId: view.HeadCommit}
	if err := c.Reviews.CreateReview(review, comments); err != nil {
		return nil, err
	}
	if c.Reviewers != nil {
		if _, err := c.Reviewers.Remove(view.Id, actorId); err != nil { // the request is answered
			return nil, err
		}
	}
	verb := map[domain.ReviewState]string{domain.ReviewApproved: "approved", domain.ReviewChangesRequested: "requested changes", domain.ReviewCommented: "reviewed"}[state]
	text := body
	for _, ic := range inline {
		text += fmt.Sprintf("\n\n%s:%d\n%s", ic.Path, ic.Line, ic.Body)
	}
	c.notify(view, actorId, verb, strings.TrimSpace(text))
	c.Webhooks.ReviewSubmitted(view, review, comments, actorId)
	return review, nil
}

// InlineComment is a comment made with a review.
type InlineComment struct {
	InlineTarget
	Body string
}

// Activity returns a pull request's reviews and comments, oldest first.
func (c *PullRequestApp) Activity(id, path *string, userId string, number int) (*PullRequestView, []domain.Review, []domain.Comment, error) {
	view, err := c.Get(id, path, userId, number)
	if err != nil {
		return nil, nil, nil, err
	}
	reviews, err := c.Reviews.Reviews(view.Id)
	if err != nil {
		return nil, nil, nil, err
	}
	comments, err := c.Reviews.Comments(view.Id)
	if err != nil {
		return nil, nil, nil, err
	}
	return view, reviews, comments, nil
}

var labelColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Labels returns a repository's labels.
func (c *PullRequestApp) RepositoryLabels(id, path *string, userId string) ([]domain.Label, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, err
	}
	return c.Labels.List(repo.Id)
}

// LabelChange holds a label's fields; nil fields are kept on update.
type LabelChange struct {
	Name, Color, Description *string
}

func (c *PullRequestApp) labelAdmin(id, path *string, actorId string) (*domain.Repository, error) {
	repo, _, role, err := c.repo(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanAdmin() {
		return nil, ErrForbidden
	}
	return repo, nil
}

func (c *PullRequestApp) CreateLabel(id, path *string, actorId string, ch LabelChange) (*domain.Label, error) {
	repo, err := c.labelAdmin(id, path, actorId)
	if err != nil {
		return nil, err
	}
	l := &domain.Label{RepositoryId: repo.Id}
	if ch.Name != nil {
		l.Name = strings.TrimSpace(*ch.Name)
	}
	if ch.Color != nil {
		l.Color = *ch.Color
	}
	if ch.Description != nil {
		l.Description = *ch.Description
	}
	if l.Name == "" || len(l.Name) > 50 || !labelColor.MatchString(l.Color) {
		return nil, ErrInvalidLabel
	}
	if err := c.Labels.Create(l); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrLabelExists
	} else if err != nil {
		return nil, err
	}
	return l, nil
}

func (c *PullRequestApp) UpdateLabel(id, path *string, actorId, labelId string, ch LabelChange) (*domain.Label, error) {
	repo, err := c.labelAdmin(id, path, actorId)
	if err != nil {
		return nil, err
	}
	l, err := c.Labels.Get(repo.Id, labelId)
	if isNotFound(err) {
		return nil, ErrLabelNotFound
	} else if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	if ch.Name != nil {
		l.Name = strings.TrimSpace(*ch.Name)
		fields["name"] = l.Name
	}
	if ch.Color != nil {
		l.Color = *ch.Color
		fields["color"] = l.Color
	}
	if ch.Description != nil {
		l.Description = *ch.Description
		fields["description"] = l.Description
	}
	if l.Name == "" || len(l.Name) > 50 || !labelColor.MatchString(l.Color) {
		return nil, ErrInvalidLabel
	}
	if err := c.Labels.Update(l.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrLabelExists
	} else if err != nil {
		return nil, err
	}
	return l, nil
}

func (c *PullRequestApp) DeleteLabel(id, path *string, actorId, labelId string) error {
	repo, err := c.labelAdmin(id, path, actorId)
	if err != nil {
		return err
	}
	if _, err := c.Labels.Get(repo.Id, labelId); isNotFound(err) {
		return ErrLabelNotFound
	} else if err != nil {
		return err
	}
	return c.Labels.Delete(labelId)
}

// SetLabels replaces a pull request's labels by name (developers and up).
func (c *PullRequestApp) SetLabels(id, path *string, actorId string, number int, names []string) ([]domain.Label, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	if !view.Role.CanWrite() {
		return nil, ErrForbidden
	}
	all, err := c.Labels.List(view.RepositoryId)
	if err != nil {
		return nil, err
	}
	byName := map[string]domain.Label{}
	for _, l := range all {
		byName[l.Name] = l
	}
	var ids []string
	var set []domain.Label
	for _, n := range names {
		l, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrLabelNotFound, n)
		}
		ids = append(ids, l.Id)
		set = append(set, l)
	}
	if err := c.Labels.Set(view.Id, ids); err != nil {
		return nil, err
	}
	c.labelEvents(view, set, actorId)
	if set == nil {
		set = []domain.Label{}
	}
	return set, nil
}

var mentionPattern = regexp.MustCompile(`(?:^|[^\w@/])@([a-z0-9][a-z0-9._-]{0,63})`)

// Mentions returns the usernames @mentioned in text.
func Mentions(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionPattern.FindAllStringSubmatch(text, -1) {
		name := strings.TrimRight(m[1], ".") // "@bob." ends a sentence
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// labelEvents queues labeled and unlabeled events for a label change.
func (c *PullRequestApp) labelEvents(view *PullRequestView, now []domain.Label, actorId string) {
	before := map[string]bool{}
	for _, l := range view.Labels {
		before[l.Id] = true
	}
	after := map[string]bool{}
	for _, l := range now {
		after[l.Id] = true
	}
	old := view.Labels
	view.Labels = now
	for _, l := range now {
		if !before[l.Id] {
			label := hookLabel(l)
			c.Webhooks.PullRequestEvent(view, "labeled", actorId, PullRequestPayload{Label: &label})
		}
	}
	for _, l := range old {
		if !after[l.Id] {
			label := hookLabel(l)
			c.Webhooks.PullRequestEvent(view, "unlabeled", actorId, PullRequestPayload{Label: &label})
		}
	}
}
