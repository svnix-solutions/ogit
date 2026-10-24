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
