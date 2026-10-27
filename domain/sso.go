package domain

import "time"

// Single sign-on provider kinds.
const (
	SSOKindOIDC   = "oidc"
	SSOKindGitHub = "github"
)

// SSO flow purposes.
const (
	SSOPurposeLogin = "login"
	SSOPurposeLink  = "link" // add a sign-in method to the signed-in user
	SSOPurposeSudo  = "sudo" // confirm the session through the provider
)

// SSOProvider is a way to sign in: an OpenID Connect issuer or GitHub
// (OAuth2). Providers from the configuration file are not stored
// (FromConfig).
type SSOProvider struct {
	Id           string `gorm:"primaryKey"`
	Slug         string // in URLs; unique
	Name         string // on the sign-in button
	Kind         string
	Issuer       string // OIDC issuer; GitHub: its base URL (https://github.com or an Enterprise server)
	ClientId     string
	ClientSecret string // sealed with security.encryption_key (plain for providers from the configuration)
	Scopes       string // extra scopes, space separated
	// AllowedDomains: email domains that may sign in, comma separated
	// ("": any).
	AllowedDomains string
	// AllowedOrgs (GitHub): organizations one of which the user must be in,
	// comma separated ("": any).
	AllowedOrgs string
	// AutoCreate: unknown identities get a new account.
	AutoCreate bool
	// RequireTwoFactor: users with oGit 2FA are asked for it after the
	// provider (otherwise the provider's own MFA is trusted).
	RequireTwoFactor bool
	Enabled          bool
	FromConfig       bool `gorm:"-"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (SSOProvider) TableName() string { return "sso_provider" }

// UserIdentity links a user to an account at a provider.
type UserIdentity struct {
	Id          string `gorm:"primaryKey"`
	UserId      string
	Provider    string // slug
	Subject     string // the provider's stable id for the account
	Email       string
	Username    string // the provider's login, for display
	LastLoginAt *time.Time
	CreatedAt   time.Time
}

func (UserIdentity) TableName() string { return "user_identity" }

// SSOFlow is a sign-in in progress at a provider, used once.
type SSOFlow struct {
	Id         string `gorm:"primaryKey"` // the state's hash
	Provider   string
	Purpose    string
	BrowserKey string // hash of the cookie that started it: the same browser finishes it
	Nonce      string
	Verifier   string // PKCE
	Redirect   string
	UserId     *string // link and sudo
	SessionId  *string // sudo
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

func (SSOFlow) TableName() string { return "sso_flow" }

// SSOPendingLink waits for the owner of an existing account (same
// verified email) to confirm with their password.
type SSOPendingLink struct {
	Id        string `gorm:"primaryKey"` // the token's hash
	UserId    string
	Provider  string
	Subject   string
	Email     string
	Username  string
	Redirect  string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func (SSOPendingLink) TableName() string { return "sso_pending_link" }

type SSOPers interface {
	Providers() ([]SSOProvider, error)
	Provider(slug string) (*SSOProvider, error)
	CreateProvider(p *SSOProvider) error
	UpdateProvider(id string, fields map[string]any) error
	DeleteProvider(id string) error

	Identity(provider, subject string) (*UserIdentity, error)
	Identities(userId string) ([]UserIdentity, error)
	CreateIdentity(i *UserIdentity) error
	TouchIdentity(id string, at time.Time) error
	DeleteIdentity(userId, id string) error

	CreateFlow(f *SSOFlow) error
	// TakeFlow deletes and returns an unexpired flow (once).
	TakeFlow(id string, now time.Time) (*SSOFlow, error)
	CreatePendingLink(l *SSOPendingLink) error
	// TakePendingLink deletes and returns an unexpired pending link.
	TakePendingLink(id string, now time.Time) (*SSOPendingLink, error)
	PendingLink(id string, now time.Time) (*SSOPendingLink, error)
	DeleteExpired(now time.Time) error
}
