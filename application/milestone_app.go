package application

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Milestone and assignee rules:
//   - anyone who can read the repository reads its milestones, with their
//     progress (open and closed issues and pull requests);
//   - maintainers and up create, edit, close, reopen and delete them;
//     deleting one leaves its issues and pull requests without a milestone;
//   - developers and up set an issue's or a pull request's milestone and
//     assignees; assignees must be able to write to the repository (at most
//     10).

var (
	ErrMilestoneNotFound = errors.New("milestone not found")
	ErrMilestoneExists   = errors.New("a milestone with this title already exists")
	ErrInvalidMilestone  = errors.New("a milestone needs a title of at most 255 characters, and a state of open or closed")
	ErrInvalidAssignee   = errors.New("assignees must be users with write access to the repository (at most 10)")
)

const maxAssignees = 10

type MilestoneApp struct {
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Milestones   domain.MilestonePers
	Webhooks     *WebhookApp
}

// MilestoneView is a milestone with its progress.
type MilestoneView struct {
	domain.Milestone
	Progress domain.MilestoneProgress
}

func (c *MilestoneApp) repo(id, path *string, userId string, admin bool) (*domain.Repository, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, err
	}
	if !admin {
		return repo, nil
	}
	if userId == "" {
		return nil, ErrForbidden
	}
	role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
	if err != nil {
		return nil, err
	}
	if !role.CanAdmin() {
		return nil, ErrForbidden
	}
	if repo.IsArchived {
		return nil, ErrGitArchived
	}
	return repo, nil
}

func (c *MilestoneApp) views(ms []domain.Milestone) ([]MilestoneView, error) {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.Id
	}
	progress, err := c.Milestones.Progress(ids)
	if err != nil {
		return nil, err
	}
	out := make([]MilestoneView, 0, len(ms))
	for _, m := range ms {
		out = append(out, MilestoneView{Milestone: m, Progress: progress[m.Id]})
	}
	return out, nil
}

// List returns milestones in state ("open" by default, "closed" or "all").
func (c *MilestoneApp) List(id, path *string, userId, state string) ([]MilestoneView, error) {
	repo, err := c.repo(id, path, userId, false)
	if err != nil {
		return nil, err
	}
	var s domain.MilestoneState
	switch state {
	case "", "open":
		s = domain.MilestoneOpen
	case "closed":
		s = domain.MilestoneClosed
	case "all":
	default:
		return nil, ErrInvalidState
	}
	ms, err := c.Milestones.List(repo.Id, s)
	if err != nil {
		return nil, err
	}
	return c.views(ms)
}

func (c *MilestoneApp) Get(id, path *string, userId, milestoneId string) (*MilestoneView, error) {
	repo, err := c.repo(id, path, userId, false)
	if err != nil {
		return nil, err
	}
	return c.view(repo, milestoneId)
}

func (c *MilestoneApp) view(repo *domain.Repository, milestoneId string) (*MilestoneView, error) {
	m, err := c.Milestones.Get(repo.Id, milestoneId)
	if isNotFound(err) {
		return nil, ErrMilestoneNotFound
	} else if err != nil {
		return nil, err
	}
	v, err := c.views([]domain.Milestone{*m})
	if err != nil {
		return nil, err
	}
	return &v[0], nil
}

// MilestoneChange holds a milestone's fields; nil ones are kept.
type MilestoneChange struct {
	Title       *string
	Description *string
	DueOn       *time.Time
	ClearDueOn  bool
	State       *string
}

func (c *MilestoneApp) Create(id, path *string, actorId string, ch MilestoneChange) (*MilestoneView, error) {
	repo, err := c.repo(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	m := &domain.Milestone{RepositoryId: repo.Id, State: domain.MilestoneOpen}
	if _, err := applyMilestone(m, ch); err != nil {
		return nil, err
	}
	if err := c.Milestones.Create(m); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrMilestoneExists
	} else if err != nil {
		return nil, err
	}
	v, err := c.view(repo, m.Id)
	if err == nil {
		c.Webhooks.MilestoneEvent(repo, &v.Milestone, v.Progress, "created", actorId, nil)
	}
	return v, err
}

func applyMilestone(m *domain.Milestone, ch MilestoneChange) (map[string]any, error) {
	fields := map[string]any{}
	if ch.Title != nil {
		m.Title = strings.TrimSpace(*ch.Title)
		fields["title"] = m.Title
	}
	if m.Title == "" || len(m.Title) > 255 {
		return nil, ErrInvalidMilestone
	}
	if ch.Description != nil {
		m.Description, fields["description"] = *ch.Description, *ch.Description
	}
	if ch.ClearDueOn {
		m.DueOn, fields["due_on"] = nil, nil
	} else if ch.DueOn != nil {
		due := ch.DueOn.UTC()
		m.DueOn, fields["due_on"] = &due, due
	}
	if ch.State != nil && domain.MilestoneState(*ch.State) != m.State {
		switch domain.MilestoneState(*ch.State) {
		case domain.MilestoneClosed:
			now := time.Now()
			m.State, m.ClosedAt = domain.MilestoneClosed, &now
		case domain.MilestoneOpen:
			m.State, m.ClosedAt = domain.MilestoneOpen, nil
		default:
			return nil, ErrInvalidMilestone
		}
		fields["state"], fields["closed_at"] = m.State, m.ClosedAt
	}
	return fields, nil
}

func (c *MilestoneApp) Update(id, path *string, actorId, milestoneId string, ch MilestoneChange) (*MilestoneView, error) {
	repo, err := c.repo(id, path, actorId, true)
	if err != nil {
		return nil, err
	}
	current, err := c.view(repo, milestoneId)
	if err != nil {
		return nil, err
	}
	m := current.Milestone
	before := m
	fields, err := applyMilestone(&m, ch)
	if err != nil {
		return nil, err
	}
	fields["updated_at"] = time.Now()
	if err := c.Milestones.Update(m.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrMilestoneExists
	} else if err != nil {
		return nil, err
	}
	v, err := c.view(repo, m.Id)
	if err != nil {
		return nil, err
	}
	changes := map[string]any{}
	if before.Title != m.Title {
		changes["title"] = map[string]string{"from": before.Title}
	}
	if before.Description != m.Description {
		changes["description"] = map[string]string{"from": before.Description}
	}
	if !slices.Equal(dueList(before.DueOn), dueList(m.DueOn)) {
		changes["due_on"] = map[string]any{"from": before.DueOn}
	}
	if len(changes) > 0 {
		c.Webhooks.MilestoneEvent(repo, &v.Milestone, v.Progress, "edited", actorId, changes)
	}
	if before.State != m.State {
		action := map[domain.MilestoneState]string{domain.MilestoneClosed: "closed", domain.MilestoneOpen: "opened"}[m.State]
		c.Webhooks.MilestoneEvent(repo, &v.Milestone, v.Progress, action, actorId, nil)
	}
	return v, nil
}

func dueList(t *time.Time) []int64 {
	if t == nil {
		return nil
	}
	return []int64{t.Unix()}
}

func (c *MilestoneApp) Delete(id, path *string, actorId, milestoneId string) error {
	repo, err := c.repo(id, path, actorId, true)
	if err != nil {
		return err
	}
	v, err := c.view(repo, milestoneId)
	if err != nil {
		return err
	}
	if err := c.Milestones.Delete(v.Id); err != nil {
		return err
	}
	c.Webhooks.MilestoneEvent(repo, &v.Milestone, v.Progress, "deleted", actorId, nil)
	return nil
}

// assignees resolves usernames to users who can write to repo.
func (c *RepositoryApp) assignees(repo *domain.Repository, names []string) ([]domain.User, error) {
	if len(names) > maxAssignees {
		return nil, ErrInvalidAssignee
	}
	var out []domain.User
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		u, err := c.UserPers.GetByUsername(name)
		if err != nil || !u.Active {
			return nil, ErrInvalidAssignee
		}
		role, err := c.GetUserEffectiveRole(repo.Id, u.Id)
		if err != nil {
			return nil, err
		}
		if !role.CanWrite() {
			return nil, ErrInvalidAssignee
		}
		out = append(out, u)
	}
	return out, nil
}

func userIds(users []domain.User) []string {
	ids := make([]string, len(users))
	for i, u := range users {
		ids[i] = u.Id
	}
	return ids
}

// assignmentDiff returns who was added and who removed.
func assignmentDiff(before, after []domain.User) (added, removed []domain.User) {
	had, has := map[string]bool{}, map[string]bool{}
	for _, u := range before {
		had[u.Id] = true
	}
	for _, u := range after {
		has[u.Id] = true
		if !had[u.Id] {
			added = append(added, u)
		}
	}
	for _, u := range before {
		if !has[u.Id] {
			removed = append(removed, u)
		}
	}
	return added, removed
}
