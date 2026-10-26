package domain

import "time"

// UserTOTP is a user's authenticator app. Secret is encrypted
// (secretbox); ConfirmedAt is nil until the user typed a first code, and
// only a confirmed TOTP turns 2FA on. LastStep is the last time step
// accepted, so a code is never accepted twice.
type UserTOTP struct {
	UserId      string `gorm:"primaryKey"`
	Secret      string
	ConfirmedAt *time.Time
	LastStep    int64
	CreatedAt   time.Time
}

func (UserTOTP) TableName() string { return "user_totp" }

// RecoveryCode is a single-use code (SHA-256 stored).
type RecoveryCode struct {
	Id        string `gorm:"primaryKey"`
	UserId    string
	CodeHash  string
	UsedAt    *time.Time
	CreatedAt time.Time
}

func (RecoveryCode) TableName() string { return "recovery_code" }

// LoginChallenge is a login waiting for its second factor: the password
// was right. Only the SHA-256 of the token given to the client is stored.
type LoginChallenge struct {
	Id        string `gorm:"primaryKey"`
	TokenHash string
	UserId    string
	Attempts  int
	UserAgent string
	IpAddress string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (LoginChallenge) TableName() string { return "login_challenge" }

// WebAuthnCredential is a security key or passkey. Data is the library's
// credential (public key, sign count, flags) as JSON; CredentialId is its
// id, base64url, for lookups at passkey sign-in.
type WebAuthnCredential struct {
	Id           string `gorm:"primaryKey"`
	UserId       string
	Name         string
	CredentialId string
	Passkey      bool // registered for passwordless sign-in
	Data         string
	LastUsedAt   *time.Time
	CreatedAt    time.Time
}

func (WebAuthnCredential) TableName() string { return "webauthn_credential" }

// WebAuthnCeremony holds the challenge of a WebAuthn registration or
// assertion until its response arrives (single use). UserId is nil for
// passkey sign-in, where the key says who it is.
type WebAuthnCeremony struct {
	Id        string `gorm:"primaryKey"`
	UserId    *string
	Kind      string // register, login, sudo, passkey
	Data      string // webauthn.SessionData, JSON
	ExpiresAt time.Time
}

func (WebAuthnCeremony) TableName() string { return "webauthn_ceremony" }

type TwoFactorPers interface {
	GetTOTP(userId string) (*UserTOTP, error)
	// SaveTOTP replaces the user's TOTP (setup restarts).
	SaveTOTP(t *UserTOTP) error
	ConfirmTOTP(userId string, step int64, at time.Time) error
	// UseStep records step as used if it is newer than the last one;
	// false when it was already used (a replay).
	UseStep(userId string, step int64) (bool, error)

	// ReplaceRecoveryCodes deletes the user's codes and stores hashes.
	ReplaceRecoveryCodes(userId string, hashes []string) error
	// UseRecoveryCode marks an unused code as used; false if none matched.
	UseRecoveryCode(userId, hash string, at time.Time) (bool, error)
	CountRecoveryCodes(userId string) (int64, error)

	// EnabledUsers returns which of userIds have 2FA on.
	EnabledUsers(userIds []string) (map[string]bool, error)

	// Disable deletes the TOTP, the recovery codes and the security keys.
	Disable(userId string) error
	// DeleteRecoveryCodes is for when the last second factor goes.
	DeleteRecoveryCodes(userId string) error

	ListCredentials(userId string) ([]WebAuthnCredential, error)
	CredentialByCredentialId(credentialId string) (*WebAuthnCredential, error)
	CreateCredential(c *WebAuthnCredential) error
	UpdateCredential(id, data string, usedAt time.Time) error
	// DeleteCredential deletes one of userId's keys; false if none matched.
	DeleteCredential(userId, id string) (bool, error)

	CreateCeremony(c *WebAuthnCeremony) error
	// TakeCeremony returns and deletes a ceremony (single use).
	TakeCeremony(id string) (*WebAuthnCeremony, error)
	DeleteExpiredCeremonies(now time.Time) (int64, error)

	CreateChallenge(c *LoginChallenge) error
	GetChallenge(tokenHash string) (*LoginChallenge, error)
	// FailChallenge counts a wrong code and returns the attempts so far.
	FailChallenge(id string) (int, error)
	DeleteChallenge(id string) error
	DeleteExpiredChallenges(now time.Time) (int64, error)
}
