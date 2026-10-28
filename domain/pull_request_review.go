package domain

import "time"

// ReviewState is a reviewer's verdict.
type ReviewState string

const (
	ReviewApproved         ReviewState = "approved"
	ReviewChangesRequested ReviewState = "changes_requested"
	ReviewCommented        ReviewState = "commented"
)

// Review is one review of a pull request, made at a commit of its source.
type Review struct {
	Id            string `gorm:"primaryKey"`
	PullRequestId string
	AuthorId      *string // nil once the account is deleted
	Author        *User   `gorm:"foreignKey:AuthorId"`
	State         ReviewState
	Body          string
	CommitId      string
	CreatedAt     time.Time
}

func (Review) TableName() string { return "pull_request_review" }

// Comment is a comment on a pull request: in the conversation, or on a line
// of its diff (Path set; Side is "old" or "new"). Comments made with a
// review have its ReviewId.
type Comment struct {
	Id            string `gorm:"primaryKey"`
	PullRequestId string
	AuthorId      *string
	Author        *User `gorm:"foreignKey:AuthorId"`
	ReviewId      *string
	Body          string
	Path          string
	Line          int
	Side          string
	CommitId      string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Comment) TableName() string { return "pull_request_comment" }

// Label is a repository's label, set on pull requests.
type Label struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Name         string
	Color        string // #rrggbb
	Description  string
	CreatedAt    time.Time
}

func (Label) TableName() string { return "label" }

type ReviewPers interface {
	CreateReview(review *Review, comments []Comment) error
	Reviews(pullRequestId string) ([]Review, error)
	CreateComment(comment *Comment) error
	Comment(pullRequestId, id string) (*Comment, error)
	UpdateComment(id, body string) error
	DeleteComment(id string) error
	Comments(pullRequestId string) ([]Comment, error)
}

type LabelPers interface {
	List(repositoryId string) ([]Label, error)
	Create(label *Label) error
	Get(repositoryId, id string) (*Label, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
	// Set replaces a pull request's labels.
	Set(pullRequestId string, labelIds []string) error
	// For returns the labels of each pull request.
	For(pullRequestIds []string) (map[string][]Label, error)
}
