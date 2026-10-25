package domain

import (
	"regexp"
	"strings"
	"time"
)

// Release annotates a tag of a repository. Drafts have no PublishedAt and
// may name a tag that doesn't exist yet (created when published, from
// TargetCommitish).
type Release struct {
	Id              string `gorm:"primaryKey"`
	RepositoryId    string
	TagName         string
	TargetCommitish string // branch or commit the tag is created from
	Name            string
	Body            string
	Draft           bool
	Prerelease      bool
	AuthorId        *string // nil once the account is deleted
	Author          *User   `gorm:"foreignKey:AuthorId"`
	PublishedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Release) TableName() string { return "repository_release" }

type ReleasePers interface {
	Create(r *Release) error
	Get(repositoryId, id string) (*Release, error)
	ByTag(repositoryId, tag string) (*Release, error)
	// List returns releases, newest first (drafts only with drafts).
	List(repositoryId string, drafts bool, offset, limit int) ([]Release, error)
	// Latest returns the newest published release that is not a
	// pre-release.
	Latest(repositoryId string) (*Release, error)
	// Previous returns the newest published release before r, if any.
	Previous(repositoryId string, before time.Time) (*Release, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
}

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9._/+@-]{1,200}$`)

// ValidTagName accepts tag names git accepts in practice: no spaces or
// control characters, no "..", no leading "-" or "/", no trailing "/",
// ".lock" or ".", no "//" or "@{".
func ValidTagName(t string) bool {
	return tagPattern.MatchString(t) && !strings.Contains(t, "..") && !strings.Contains(t, "//") && !strings.Contains(t, "@{") &&
		!strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "/") && !strings.HasSuffix(t, "/") &&
		!strings.HasSuffix(t, ".lock") && !strings.HasSuffix(t, ".") && t != "@"
}
