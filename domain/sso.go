package domain

import "time"

// Single sign-on provider kinds.
const (
	SSOKindOIDC   = "oidc"
	SSOKindGitHub = "github"
	SSOKindLDAP   = "ldap" // LDAP or Active Directory: the sign-in form
	SSOKindSAML   = "saml" // SAML 2.0 identity providers
)

// SSO flow purposes.
// SettingSSOOnly ("true"/"false"): sign in only through providers.
const SettingSSOOnly = "sso_only"

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
	// GroupsClaim: the claim listing the user's groups ("groups" when
	// empty; a dotted path reaches nested claims).
	GroupsClaim string
	// AdminGroup: members of this group are site admins.
	AdminGroup string
	// GroupMappings: JSON list of SSOGroupMapping.
	GroupMappings string
	// LDAPConfig (kind ldap): LDAPSettings as JSON. Issuer is the URL
	// (ldap:// or ldaps://), ClientId and ClientSecret the service
	// account's DN and password (empty: anonymous search).
	LDAPConfig string `gorm:"column:ldap_config"`
	// SAMLConfig (kind saml): SAMLSettings as JSON. Issuer is the IdP's
	// metadata URL ("" with metadata_xml), ClientSecret the sealed SP
	// private key (PEM).
	SAMLConfig string `gorm:"column:saml_config"`
	Enabled    bool
	FromConfig bool `gorm:"-"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (SSOProvider) TableName() string { return "sso_provider" }

// LDAPSettings say how to find users and their groups in a directory.
type LDAPSettings struct {
	UserBase string `json:"user_base" yaml:"user_base"`
	// UserFilter finds the user; {input} is what they typed (escaped).
	UserFilter        string `json:"user_filter" yaml:"user_filter"`
	UsernameAttribute string `json:"username_attribute" yaml:"username_attribute"` // uid; AD: sAMAccountName
	EmailAttribute    string `json:"email_attribute" yaml:"email_attribute"`       // mail
	NameAttribute     string `json:"name_attribute" yaml:"name_attribute"`         // cn
	// SubjectAttribute: a stable id (entryUUID; AD: objectGUID); empty:
	// the DN.
	SubjectAttribute string `json:"subject_attribute" yaml:"subject_attribute"`
	// Groups: from MemberOfAttribute on the user's entry (AD: memberOf),
	// else searched under GroupBase with GroupFilter ({dn}, {username}).
	MemberOfAttribute  string `json:"member_of_attribute" yaml:"member_of_attribute"`
	GroupBase          string `json:"group_base" yaml:"group_base"`
	GroupFilter        string `json:"group_filter" yaml:"group_filter"`
	GroupNameAttribute string `json:"group_name_attribute" yaml:"group_name_attribute"` // cn
	StartTLS           bool   `json:"start_tls" yaml:"start_tls"`
	// RootCA: PEM certificates to trust (else the system's).
	RootCA             string `json:"root_ca" yaml:"root_ca"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify" yaml:"insecure_skip_verify"`
}

// SAMLSettings configure a SAML identity provider.
type SAMLSettings struct {
	// MetadataXML: the IdP's metadata pasted (instead of a URL).
	MetadataXML string `json:"metadata_xml" yaml:"metadata_xml"`
	// AllowIdPInitiated accepts sign-ins started at the IdP (its
	// dashboard), which can't be tied to the browser.
	AllowIdPInitiated bool   `json:"allow_idp_initiated" yaml:"allow_idp_initiated"`
	UsernameAttribute string `json:"username_attribute" yaml:"username_attribute"`
	EmailAttribute    string `json:"email_attribute" yaml:"email_attribute"`
	NameAttribute     string `json:"name_attribute" yaml:"name_attribute"`
	// SubjectAttribute: a stable id attribute; empty: the NameID.
	SubjectAttribute string `json:"subject_attribute" yaml:"subject_attribute"`
	// Certificate: the SP's certificate (PEM), generated with the provider.
	Certificate string `json:"certificate" yaml:"certificate"`
}

// SSOGroupMapping makes members of an IdP group members of an oGit group.
type SSOGroupMapping struct {
	Group  string    `json:"group" yaml:"group"`   // at the provider
	Target string    `json:"target" yaml:"target"` // oGit group path
	Role   GroupRole `json:"role" yaml:"role"`     // member or maintainer
}

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
	// Result (SAML): the verified claims (JSON) the assertion consumer
	// leaves for the browser's next step, which checks the cookie.
	Result    string
	ExpiresAt time.Time
	CreatedAt time.Time
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
	Groups    string // JSON: the provider's groups, synced once linked
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
	// Flow returns an unexpired flow without taking it.
	Flow(id string, now time.Time) (*SSOFlow, error)
	SetFlowResult(id, result string) error
	// UseAssertion records a SAML assertion's id until expires; false if
	// it was used already (a replay).
	UseAssertion(id string, expires time.Time) (bool, error)
	CreatePendingLink(l *SSOPendingLink) error
	// TakePendingLink deletes and returns an unexpired pending link.
	TakePendingLink(id string, now time.Time) (*SSOPendingLink, error)
	PendingLink(id string, now time.Time) (*SSOPendingLink, error)
	DeleteExpired(now time.Time) error
}
