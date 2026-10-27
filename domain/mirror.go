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

// Push mirror modes.
const (
	// PushMirrorForce makes the remote's branches and tags identical
	// (force, deleting the others), like git push --mirror.
	PushMirrorForce = "mirror"
	// PushMirrorBranches pushes branches and tags without forcing or
	// deleting: diverged refs are reported, the remote's others kept.
	PushMirrorBranches = "branches"
)

// MirrorRemote is a remote and how to authenticate to it (pull and push
// mirrors).
type MirrorRemote struct {
	URL      string
	Auth     string
	Username string
	// Secret is the sealed password or token, or SSH private key.
	Secret       string
	SSHPublicKey string `gorm:"column:ssh_public_key"`
	// SSHHostKey is the remote's host key, pinned on first connection.
	SSHHostKey string `gorm:"column:ssh_host_key"`
}

// Mirror is a repository's pull mirror (Mirror true: keeps syncing,
// read-only) or import in progress (false: syncs once).
type Mirror struct {
	Id              string `gorm:"primaryKey"`
	RepositoryId    string
	MirrorRemote    `gorm:"embedded"`
	Mirror          bool
	LFS             bool `gorm:"column:lfs"`
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

// PushMirror pushes a repository's branches and tags to a remote after
// each push and every interval.
type PushMirror struct {
	Id              string `gorm:"primaryKey"`
	RepositoryId    string
	MirrorRemote    `gorm:"embedded"`
	Mode            string
	LFS             bool `gorm:"column:lfs"`
	IntervalSeconds int
	Status          string
	LastError       string
	Failures        int
	// Generation counts the pushes that made the mirror due: a sync that
	// started before one (generation changed) leaves it due.
	Generation    int
	LastSyncAt    *time.Time
	LastSuccessAt *time.Time
	NextSyncAt    time.Time
	LockedUntil   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (PushMirror) TableName() string { return "repository_push_mirror" }

type PushMirrorPers interface {
	Create(m *PushMirror) error
	Get(repositoryId, id string) (*PushMirror, error)
	List(repositoryId string) ([]PushMirror, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error
	// Claim is MirrorPers.Claim for push mirrors.
	Claim(now, lockUntil time.Time) (*PushMirror, error)
	// Touch makes a repository's push mirrors due by now and bumps their
	// generation.
	Touch(repositoryId string, now time.Time) error
	// Schedule sets next_sync_at unless the generation changed since the
	// claim.
	Schedule(id string, generation int, next time.Time) error
}
