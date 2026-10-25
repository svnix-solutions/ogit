package domain

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"github.com/gofiber/fiber/v2/utils"
	"gorm.io/gorm"
)

type Token struct {
	Id          string
	Name        string
	Description string
	Token       string

	Type TokenType

	UserId *string
	User   User `gorm:"foreignKey:UserId;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	RepositoryId *string
	Repository   Repository `gorm:"foreignKey:RepositoryId;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	Active bool

	Scopes TokenScopes

	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	// CreatedById is who made a repository token (nil once deleted).
	CreatedById *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Token) TableName() string {
	return "token"
}

func (t *Token) BeforeCreate(tx *gorm.DB) error {
	t.Id = utils.UUIDv4()
	return nil
}

type TokenType string

const (
	TokenUser TokenType = "user"
	TokenRepo TokenType = "repository" // must match the token.type constraint in the migration
)

type TokenScopes []TokenScope

type TokenScope string

const (
	ScopeReadRepository     TokenScope = "read:repository"
	ScopeWriteRepository    TokenScope = "write:repository"
	ScopeAdminRepository    TokenScope = "admin:repository"
	ScopeCreatePullRequest  TokenScope = "create:pull_request"
	ScopeMergePullRequest   TokenScope = "merge:pull_request"
	ScopeCommentPullRequest TokenScope = "comment:pull_request"
	// ScopeWriteStatus posts commit statuses (CI) without push access.
	ScopeWriteStatus TokenScope = "write:status"
)

func (t TokenScopes) Value() (driver.Value, error) {
	valueString, err := json.Marshal(t)
	return string(valueString), err
}

// Scan accepts both string (sqlite TEXT) and []byte (postgres JSONB).
func (t *TokenScopes) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return json.Unmarshal([]byte(v), t)
	case []byte:
		return json.Unmarshal(v, t)
	default:
		return fmt.Errorf("unsupported type for TokenScopes: %T", value)
	}
}

type TokenPers interface {
	Create(token *Token) error
	GetByNameAndToken(name, tokenStr string) (*Token, error)
	// GetByToken finds a token by its hash alone (API calls send no name).
	GetByToken(tokenStr string) (*Token, error)
	GetByUserId(userId string) ([]Token, error)
	GetById(id string) (*Token, error)
	// GetByRepositoryId returns a repository's tokens.
	GetByRepositoryId(repositoryId string) ([]Token, error)
	// Touch records that the token was used.
	Touch(id string, at time.Time) error
	Delete(token *Token) error
}

// A repository token reads its repository's API as a principal of its own,
// "repo-token:<repository id>": a reader of that repository only, and
// never a person (it can't comment, review or be assigned).
const repoTokenPrefix = "repo-token:"

func RepoTokenPrincipal(repositoryId string) string { return repoTokenPrefix + repositoryId }

// RepoTokenRepository returns the repository of a repository token
// principal.
func RepoTokenRepository(principal string) (string, bool) {
	if len(principal) > len(repoTokenPrefix) && principal[:len(repoTokenPrefix)] == repoTokenPrefix {
		return principal[len(repoTokenPrefix):], true
	}
	return "", false
}

// IsPerson reports whether id is a signed-in user (not anonymous, not a
// repository token).
func IsPerson(id string) bool {
	_, token := RepoTokenRepository(id)
	return id != "" && !token
}
