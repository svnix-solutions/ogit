package domain

import "time"

// Mirror auth kinds.
const (
	MirrorAuthNone     = "none"
	MirrorAuthPassword = "password"
	MirrorAuthSSH      = "ssh"
)

// Mirror sync statuses.
const (
	MirrorPending = "pending"
	MirrorSyncing = "syncing"
	MirrorOK      = "ok"
	MirrorFailed  = "failed"
)

// Mirror is a repository's pull mirror (Mirror true: keeps syncing,
// read-only) or import in progress (false: syncs once).
type Mirror struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	URL          string
	Mirror       bool
	Auth         string
	Username     string
	// Secret is the sealed password or token, or SSH private key.
	Secret       string
	SSHPublicKey string `gorm:"column:ssh_public_key"`
	// SSHHostKey is the remote's host key, pinned on first connection.
	SSHHostKey      string `gorm:"column:ssh_host_key"`
	LFS             bool   `gorm:"column:lfs"`
	IntervalSeconds int
	Status          string
	LastError       string
	Failures        int
	LastSyncAt      *time.Time
	LastSuccessAt   *time.Time
	NextSyncAt      time.Time
	LockedUntil     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Mirror) TableName() string { return "repository_mirror" }

type MirrorPers interface {
	Create(m *Mirror) error
	ForRepository(repositoryId string) (*Mirror, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
	// Claim locks a due mirror (next_sync_at <= now, not locked or the
	// lock expired) until lockUntil; nil when none is due. Safe across
	// servers.
	Claim(now, lockUntil time.Time) (*Mirror, error)
}
