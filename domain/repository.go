package domain

import "time"

type Repository struct {
	Id          string `gorm:"primaryKey"`
	Name        string
	Description string
	Slug        string
	// StorageKey is where the git data lives: the id for repositories created
	// after renames were introduced, the original slug for older ones. It
	// never changes, so renaming does not move data.
	StorageKey string

	DefaultBranch string

	IsArchived bool
	ArchivedAt *time.Time

	// No gorm default here: with one, GORM omits false on insert and the
	// column default (true) wins, so public repositories could not be created.
	// Private-by-default is applied by the API when is_private is omitted.
	IsPrivate bool

	// DeployKeys DeployKeys `gorm:"foreignKey:RepositoryId"`
	// WebHooks   WebHooks   `gorm:"foreignKey:RepositoryId"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Repository) TableName() string {
	return "repository"
}

// DeployKeys represents a list of deploy keys for a repository.
type DeployKeys []DeployKey

type DeployKey struct {
	Name       string
	Key        string
	AccessType AccessType
	CreatedAt  time.Time
}

// WebHook represents a webhook for a repository.
type WebHooks []WebHook
type WebHook struct {
	Id          string
	Url         string
	ContentType ContentType
	Secret      string
	SSLVerify   bool
	Events      Events
	Actived     bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

type ContentType string
type JsonContentType ContentType

type Events []Event

type Event string
type AllEvents Event
type PushEvent Event
type PullRequestEvent Event
type IssuesEvent Event
type IssueCommentEvent Event

type RepositoryPers interface {
	GetRepository(repo Repository) (*Repository, error)
	Create(repo *Repository) error
	Delete(repo Repository) error
	// Purge deletes the repository and every row that references it
	// (members, repository tokens, deploy keys) in one transaction.
	Purge(id string) error
	List(limit, offset int) ([]Repository, int64, error)
	// SoleOwnedBy returns the repositories where userId is the only owner.
	SoleOwnedBy(userId string) ([]Repository, error)
	// Rename changes name and slug in one transaction: the old slug becomes
	// a redirect, and any redirect that pointed the new slug elsewhere is
	// dropped (the slug now belongs to this repository).
	Rename(id, name, newSlug, oldSlug string) error
	// Update sets the given columns.
	Update(id string, fields map[string]any) error
	// DropRedirect removes the redirect for slug, if any.
	DropRedirect(slug string) error
	// ResolveRedirect returns the repository an old slug now points to.
	ResolveRedirect(slug string) (*Repository, error)
	// ListPublic returns public repositories whose name or description
	// contains query (all when empty), with the total.
	ListPublic(query string, limit, offset int) ([]Repository, int64, error)
}

// RepositoryRedirect maps a former slug to a renamed repository.
type RepositoryRedirect struct {
	Slug         string `gorm:"primaryKey"`
	RepositoryId string
	CreatedAt    time.Time
}

func (RepositoryRedirect) TableName() string {
	return "repository_redirect"
}
