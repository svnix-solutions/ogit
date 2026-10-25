package domain

import "time"

type Repository struct {
	Id          string `gorm:"primaryKey"`
	Name        string
	Description string
	// Namespace is the owner's path (a username or a group name), the first
	// segment of the repository's path: <namespace>/<slug>. Usernames and
	// group names never change, so it is stored here. Slugs are unique per
	// namespace.
	Namespace string
	OwnerType OwnerType
	OwnerId   string
	Slug      string
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

	// RequiredApprovals is how many approvals (from developers and up, not
	// the author) a pull request needs before it can be merged.
	RequiredApprovals int

	// HasIssues: the repository uses issues (owners turn them off; existing
	// ones stay readable). No gorm default, for the reason above: callers
	// set it (true for new repositories).
	HasIssues bool

	// ForkOfId is the repository this one was forked from (nil when it
	// isn't a fork, or the original was deleted).
	ForkOfId *string

	// DeployKeys DeployKeys `gorm:"foreignKey:RepositoryId"`
	// WebHooks   WebHooks   `gorm:"foreignKey:RepositoryId"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Repository) TableName() string {
	return "repository"
}

// FullPath is how the repository is addressed: <namespace>/<slug>, in git
// URLs (/<namespace>/<slug>.git), the API and the web UI.
func (r Repository) FullPath() string {
	return r.Namespace + "/" + r.Slug
}

// OwnerType says whether a repository's namespace is a user or a group.
type OwnerType string

const (
	OwnerUser  OwnerType = "user"
	OwnerGroup OwnerType = "group"
)

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
	// GetByPath returns the repository at namespace/slug.
	GetByPath(namespace, slug string) (*Repository, error)
	// InNamespace returns the repositories a user or group owns.
	InNamespace(ownerType OwnerType, ownerId string) ([]Repository, error)
	// ForksOf returns the repositories forked from repositoryId.
	ForksOf(repositoryId string) ([]Repository, error)
	Create(repo *Repository) error
	Delete(repo Repository) error
	// Purge deletes the repository and every row that references it
	// (members, repository tokens, deploy keys) in one transaction.
	Purge(id string) error
	List(limit, offset int) ([]Repository, int64, error)
	// SoleOwnedBy returns the repositories where userId is the only owner.
	SoleOwnedBy(userId string) ([]Repository, error)
	// Rename changes name and slug in one transaction: the old path becomes
	// a redirect, and any redirect that pointed the new path elsewhere is
	// dropped (the path now belongs to this repository).
	Rename(id, namespace, name, newSlug, oldSlug string) error
	// Move puts the repository in another namespace in one transaction: the
	// old path (oldNamespace/slug) becomes a redirect, and a redirect that
	// pointed the new path elsewhere is dropped.
	Move(id, oldNamespace, slug string, to Namespace) error
	// Update sets the given columns.
	Update(id string, fields map[string]any) error
	// DropRedirect removes the redirect for namespace/slug, if any.
	DropRedirect(namespace, slug string) error
	// ResolveRedirect returns the repository a former path now points to.
	ResolveRedirect(namespace, slug string) (*Repository, error)
	// ListPublic returns public repositories whose name or description
	// contains query (all when empty), with the total.
	ListPublic(query string, limit, offset int) ([]Repository, int64, error)
}

// RepositoryRedirect maps a former path (namespace/slug) to a renamed
// repository.
type RepositoryRedirect struct {
	Namespace    string `gorm:"primaryKey"`
	Slug         string `gorm:"primaryKey"`
	RepositoryId string
	CreatedAt    time.Time
}

func (RepositoryRedirect) TableName() string {
	return "repository_redirect"
}
