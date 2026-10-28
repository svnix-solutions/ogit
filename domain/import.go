package domain

import "time"

// Import statuses.
const (
	ImportWaiting = "waiting" // for the git data (the first sync)
	ImportPending = "pending"
	ImportRunning = "running"
	ImportDone    = "done"
	ImportFailed  = "failed"
)

// Import stages, in order.
const (
	StageLabels     = "labels"
	StageMilestones = "milestones"
	StageIssues     = "issues"
	StagePulls      = "pulls"
	StageReleases   = "releases"
	StageDone       = "done"
)

// Import copies a repository's issues, pull requests, labels, milestones
// and releases from a forge's API, once, after its git data.
type Import struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	// ActorId ran the import: authors that match no user are them (with
	// a note naming the original author).
	ActorId string
	Forge   string // forge.GitHub, forge.GitLab, forge.Gitea
	APIURL  string `gorm:"column:api_url"`
	Project string
	Secret  string // the sealed API token
	Status  string
	Stage   string
	// Reserved: the repository's numbers up to here were reserved for the
	// imported issues and pull requests; PullOffset is added to pull
	// request numbers (GitLab numbers them apart from issues).
	Reserved    int
	PullOffset  int
	Issues      int
	Pulls       int
	Releases    int
	Comments    int
	LastError   string
	Warnings    string
	Failures    int
	NextRunAt   time.Time
	LockedUntil *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Import) TableName() string { return "repository_import" }

// ImportedReview is a review and its inline comments.
type ImportedReview struct {
	Review   Review
	Comments []Comment
}

type ImportPers interface {
	Create(i *Import) error
	ForRepository(repositoryId string) (*Import, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
	// Ready makes a waiting import due (its git data is in).
	Ready(repositoryId string, now time.Time) error
	// Claim locks a due import (pending, or running with an expired lock)
	// until lockUntil; nil when none is due.
	Claim(now, lockUntil time.Time) (*Import, error)
	// Reserve raises the repository's last issue/pull request number to n.
	Reserve(repositoryId string, n int) error
	// AddIssue writes an issue with its comments, labels and assignees in
	// one transaction, under its number (number 0: the next free one);
	// false if that number is taken.
	AddIssue(issue *Issue, comments []IssueComment, labelIds, assigneeIds []string) (bool, error)
	// AddPull does the same for a pull request, with its reviews.
	AddPull(pr *PullRequest, comments []Comment, reviews []ImportedReview, labelIds, assigneeIds []string) (bool, error)
}
