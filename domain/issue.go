package domain

import "time"

type IssueState string

const (
	IssueOpen   IssueState = "open"
	IssueClosed IssueState = "closed"
)

// Issue is numbered in the same sequence as the repository's pull requests.
type Issue struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Number       int
	Title        string
	Body         string
	AuthorId     *string // nil once the account is deleted
	Author       *User   `gorm:"foreignKey:AuthorId"`
	State        IssueState
	ClosedAt     *time.Time
	ClosedById   *string
	ClosedBy     *User `gorm:"foreignKey:ClosedById"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Issue) TableName() string { return "issue" }

type IssueComment struct {
	Id        string `gorm:"primaryKey"`
	IssueId   string
	AuthorId  *string
	Author    *User `gorm:"foreignKey:AuthorId"`
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (IssueComment) TableName() string { return "issue_comment" }

// IssueFilter selects issues in List.
type IssueFilter struct {
	State    IssueState // "" for all
	LabelId  string
	AuthorId string
	Query    string // in the title or the body
}

type IssuePers interface {
	// Create gives the issue the repository's next number (shared with pull
	// requests) and stores it.
	Create(issue *Issue) error
	Get(repositoryId string, number int) (*Issue, error)
	// List returns the issues matching f, newest first, with the number of
	// matching issues in each state (f.State aside).
	List(repositoryId string, f IssueFilter, offset, limit int) ([]Issue, map[IssueState]int64, error)
	// Update sets fields on the issue.
	Update(id string, fields map[string]any) error
	Delete(id string) error

	CreateComment(c *IssueComment) error
	Comment(issueId, id string) (*IssueComment, error)
	Comments(issueId string) ([]IssueComment, error)
	UpdateComment(id, body string) error
	DeleteComment(id string) error

	// SetLabels replaces an issue's labels; Labels returns them per issue.
	SetLabels(issueId string, labelIds []string) error
	Labels(issueIds []string) (map[string][]Label, error)
}
