package domain

import "time"

// StatusState is a commit status's state, as on GitHub.
type StatusState string

const (
	StatusPending StatusState = "pending"
	StatusSuccess StatusState = "success"
	StatusFailure StatusState = "failure"
	StatusError   StatusState = "error"
)

func (s StatusState) Valid() bool {
	return s == StatusPending || s == StatusSuccess || s == StatusFailure || s == StatusError
}

// CommitStatus is one report (by CI, usually) on a commit, under a context
// such as "ci/build". Every report is kept; the latest per context counts.
type CommitStatus struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	SHA          string `gorm:"column:sha"`
	State        StatusState
	Context      string
	Description  string
	TargetURL    string  `gorm:"column:target_url"`
	CreatorId    *string // nil once the account is deleted
	Creator      *User   `gorm:"foreignKey:CreatorId"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (CommitStatus) TableName() string { return "commit_status" }

type CommitStatusPers interface {
	Create(s *CommitStatus) error
	// List returns a commit's statuses, newest first.
	List(repositoryId, sha string, limit int) ([]CommitStatus, error)
	// Latest returns the latest status per context of each commit.
	Latest(repositoryId string, shas []string) (map[string][]CommitStatus, error)
	// Contexts returns the contexts reported in the repository since then,
	// most recent first.
	Contexts(repositoryId string, since time.Time, limit int) ([]string, error)
}

// CombinedState is GitHub's combined status: failure if any context failed
// or errored, else pending if any is pending (or there are none), else
// success.
func CombinedState(latest []CommitStatus) StatusState {
	if len(latest) == 0 {
		return StatusPending
	}
	state := StatusSuccess
	for _, s := range latest {
		switch s.State {
		case StatusFailure, StatusError:
			return StatusFailure
		case StatusPending:
			state = StatusPending
		}
	}
	return state
}
