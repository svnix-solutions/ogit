package domain

import (
	"errors"
	"time"
)

// ErrWorkspaceExists: the user already has a workspace on that branch.
var ErrWorkspaceExists = errors.New("you already have a workspace on this branch")

type WorkspaceState string

// Starting and running workspaces, and those being stopped, count against
// the user's limit. Agents move them to running and stopping.
const (
	WorkspaceStopped  WorkspaceState = "stopped"
	WorkspaceStarting WorkspaceState = "starting"
	WorkspaceRunning  WorkspaceState = "running"
	WorkspaceStopping WorkspaceState = "stopping"
)

// Workspace is a user's development container for a branch.
type Workspace struct {
	Id           string `gorm:"primaryKey"`
	RepositoryId string
	UserId       string
	Branch       string
	State        WorkspaceState
	LastUsedAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Workspace) TableName() string { return "workspace" }

type WorkspacePers interface {
	Create(w *Workspace) error
	// Get returns the user's workspace.
	Get(userId, id string) (*Workspace, error)
	// List returns the user's workspaces, of one repository unless
	// repositoryId is empty, the most recently used first.
	List(userId, repositoryId string) ([]Workspace, error)
	// Start moves a stopped workspace to starting if the user has fewer
	// than max active ones. False means it didn't: check the state.
	Start(userId, id string, max int, now time.Time) (bool, error)
	// Stop stops a workspace that is starting or running.
	Stop(userId, id string, now time.Time) error
	// Delete removes a stopped workspace. False means it wasn't stopped.
	Delete(userId, id string) (bool, error)
}
