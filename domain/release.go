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
	Id string `gorm:"primaryKey"`
	// ApiId is the numeric id of the GitHub-compatible API (its clients
	// parse ids as integers): random, below 2^53 for JavaScript.
	ApiId           int64
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
	ByApiId(repositoryId string, apiId int64) (*Release, error)
	// List returns releases, newest first (drafts only with drafts).
	List(repositoryId string, drafts bool, offset, limit int) ([]Release, error)
	Count(repositoryId string, drafts bool) (int64, error)
	// Latest returns the newest published release that is not a
	// pre-release.
	Latest(repositoryId string) (*Release, error)
	// Previous returns the newest published release before r, if any.
	Previous(repositoryId string, before time.Time) (*Release, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
}

// ReleaseAsset is a file attached to a release, stored with the
// repository's data (storage.Config.PutAsset, by Id).
type ReleaseAsset struct {
	Id            string `gorm:"primaryKey"`
	ApiId         int64
	ReleaseId     string
	RepositoryId  string
	Name          string
	Label         string
	ContentType   string
	Size          int64
	DownloadCount int64
	UploaderId    *string // nil for repository tokens and deleted accounts
	Uploader      *User   `gorm:"foreignKey:UploaderId"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (ReleaseAsset) TableName() string { return "release_asset" }

type ReleaseAssetPers interface {
	Create(a *ReleaseAsset) error
	Get(repositoryId, id string) (*ReleaseAsset, error)
	ByApiId(repositoryId string, apiId int64) (*ReleaseAsset, error)
	ByName(releaseId, name string) (*ReleaseAsset, error)
	// List returns the assets of releases, by name.
	List(releaseIds ...string) ([]ReleaseAsset, error)
	Update(id string, fields map[string]any) error
	CountDownload(id string) error
	Delete(id string) error
}

var assetName, dots = regexp.MustCompile(`[^A-Za-z0-9._+@~-]+`), regexp.MustCompile(`\.{2,}`)

// AssetName makes a file name safe like GitHub does: other characters
// become ".", runs of dots one, and it can't start with "." (no hidden
// files, no "..").
func AssetName(name string) string {
	name = assetName.ReplaceAllString(strings.TrimSpace(name), ".")
	name = strings.TrimLeft(dots.ReplaceAllString(name, "."), ".")
	if len(name) > 255 {
		name = name[:255]
	}
	return name
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
