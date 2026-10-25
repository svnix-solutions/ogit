package domain

import "time"

// PullRequest asks to merge one branch of a repository into another.
// Numbers are per repository (#1, #2, ...).
type PullRequest struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Number       int
	Title        string
	Body         string
	AuthorId     *string // nil once the author's account is deleted
	Author       *User   `gorm:"foreignKey:AuthorId"`
	SourceBranch string
	TargetBranch string
	State        PullRequestState
	Draft        bool

	// Recorded when the pull request is merged or closed, so its commits
	// and diff stay viewable after the branches move or are deleted.
	BaseCommit string // target branch tip
	HeadCommit string // source branch tip

	MergeCommit string
	MergeMethod string
	MergedById  *string
	MergedBy    *User `gorm:"foreignKey:MergedById"`
	MergedAt    *time.Time
	ClosedAt    *time.Time
	MilestoneId *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (PullRequest) TableName() string { return "pull_request" }

type PullRequestState string

const (
	PullRequestOpen   PullRequestState = "open"
	PullRequestClosed PullRequestState = "closed"
	PullRequestMerged PullRequestState = "merged"
)

type PullRequestPers interface {
	// Create assigns the next number in the repository and stores pr.
	Create(pr *PullRequest) error
	Get(repositoryId string, number int) (*PullRequest, error)
	// List returns a repository's pull requests in a state ("" for all) and
	// with a label ("" for any), newest first, with the number of pull
	// requests in each state.
	List(repositoryId string, state PullRequestState, labelId string, offset, limit int) ([]PullRequest, map[PullRequestState]int64, error)
	// OpenFor returns the open pull request from source into target, if any.
	OpenFor(repositoryId, source, target string) (*PullRequest, error)
	// Update sets fields on pr if its state is still from; false when it
	// changed meanwhile (another request merged or closed it).
	Update(id string, from PullRequestState, fields map[string]any) (bool, error)
}
