package domain

import (
	"time"
)

// SSHKey is a public key that can authenticate git over SSH: a user key
// (acts as the user) or a deploy key (one repository, possibly read-only).
// Each key (by fingerprint) is used in exactly one place.
type SSHKey struct {
	Id          string
	Name        string
	Fingerprint string // SHA256:..., unique
	PublicKey   string // authorized_keys format, without comment
	KeyType     SSHType
	OwnerType   SSHKeyOwnerType

	UserId *string
	User   User `gorm:"foreignKey:UserId"`

	RepositoryId *string
	Repository   Repository `gorm:"foreignKey:RepositoryId"`
	ReadOnly     bool       // deploy keys only

	CreatedBy  *string // user who added the key
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

func (SSHKey) TableName() string {
	return "ssh_key"
}

type SSHType string

const (
	SSHTypeED25519 SSHType = "ed25519"
	SSHTypeRSA     SSHType = "rsa"
	SSHTypeECDSA   SSHType = "ecdsa"
)

type SSHKeyOwnerType string

const (
	SSHKeyTypeUser       SSHKeyOwnerType = "user"
	SSHKeyTypeRepository SSHKeyOwnerType = "repository"
)

type SSHKeyPers interface {
	Create(key *SSHKey) error
	// GetByFingerprint loads the key with its User or Repository.
	GetByFingerprint(fingerprint string) (*SSHKey, error)
	ListForUser(userId string) ([]SSHKey, error)
	ListForRepository(repositoryId string) ([]SSHKey, error)
	DeleteForUser(userId, keyId string) error
	DeleteForRepository(repositoryId, keyId string) error
	TouchLastUsed(keyId string, at time.Time) error
}
