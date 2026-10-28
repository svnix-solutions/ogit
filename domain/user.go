package domain

import "time"

type User struct {
	Id       string
	Username string
	Email    string
	Password string

	AvatarUrl   string
	Preferences JSONB
	Active      bool

	Role Role `gorm:"type:role;default:'user'"`
	// RoleSynced: the role was set by a sign-in provider's admin group
	// (which may take it back).
	RoleSynced bool `gorm:"column:role_synced"`

	// EmailVerifiedAt is nil while the email address is unverified.
	EmailVerifiedAt *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (u *User) TableName() string {
	return "user"
}

type UserPers interface {
	GetById(id string) (User, error)
	GetByUsername(username string) (User, error)
	GetByEmail(email string) (User, error)
	Create(user *User) error
	MarkEmailVerified(userId string, at time.Time) error
	// List returns users whose username or email contains query (all when
	// empty), ordered by username, with the total number of matches.
	List(query string, limit, offset int) ([]User, int64, error)
	SetActive(userId string, active bool) error
	// SetRole sets a role by hand (no longer synced).
	SetRole(userId string, role Role) error
	// SetSyncedRole sets a role from a provider's admin group.
	SetSyncedRole(userId string, role Role) error
	SetPassword(userId, hash string) error
	CountActiveAdmins() (int64, error)
	// Purge deletes the user and every row that references them (sessions,
	// tokens, memberships, pending verification, SSH keys) in one transaction.
	Purge(userId string) error
}

// EmailVerification is a pending email verification. A user has at most one;
// only the SHA-256 of the token sent by email is stored.
type EmailVerification struct {
	Id        string
	UserId    string
	TokenHash string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (EmailVerification) TableName() string {
	return "email_verification"
}

type EmailVerificationPers interface {
	// Replace stores v as the user's only pending verification.
	Replace(v *EmailVerification) error
	GetByTokenHash(hash string) (*EmailVerification, error)
	GetByUserId(userId string) (*EmailVerification, error)
	DeleteByUserId(userId string) error
}
