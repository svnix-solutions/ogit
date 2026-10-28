package domain

import "time"

type MilestoneState string

const (
	MilestoneOpen   MilestoneState = "open"
	MilestoneClosed MilestoneState = "closed"
)

// Milestone groups a repository's issues and pull requests.
type Milestone struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Title        string
	Description  string
	DueOn        *time.Time
	State        MilestoneState
	ClosedAt     *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Milestone) TableName() string { return "milestone" }

// MilestoneProgress counts a milestone's issues and pull requests: open,
// and closed (merged pull requests included).
type MilestoneProgress struct {
	Open, Closed int64
}

type MilestonePers interface {
	// List returns a repository's milestones in a state ("" for all), the
	// soonest due first (those without a due date last).
	List(repositoryId string, state MilestoneState) ([]Milestone, error)
	Get(repositoryId, id string) (*Milestone, error)
	Create(m *Milestone) error
	Update(id string, fields map[string]any) error
	// Delete removes the milestone; its issues and pull requests keep going
	// without one.
	Delete(id string) error
	Progress(milestoneIds []string) (map[string]MilestoneProgress, error)
}

// Assignment kinds: whose assignees.
const (
	AssignIssue       = "issue"
	AssignPullRequest = "pull_request"
)

type AssigneePers interface {
	// Set replaces the assignees of an issue or a pull request (kind).
	Set(kind, itemId string, userIds []string) error
	// For returns the assignees of items, by item id.
	For(kind string, itemIds []string) (map[string][]User, error)
}
