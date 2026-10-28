package domain

import "time"

// LFSLock is a Git LFS file lock: only its owner may change the file.
type LFSLock struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	Path         string
	OwnerId      string
	Owner        User   `gorm:"foreignKey:OwnerId"`
	Ref          string // as given by the client; informational
	CreatedAt    time.Time
}

func (LFSLock) TableName() string { return "lfs_lock" }

type LFSLockPers interface {
	Create(l *LFSLock) error
	Get(repositoryId, id string) (*LFSLock, error)
	ByPath(repositoryId, path string) (*LFSLock, error)
	// List returns locks by path, from cursor (a lock id) on, at most limit.
	List(repositoryId, path, id, cursor string, limit int) ([]LFSLock, error)
	// All returns every lock of a repository, by path (push checks).
	All(repositoryId string) ([]LFSLock, error)
	Delete(id string) error
}
