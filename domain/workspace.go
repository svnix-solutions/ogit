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
	// AgentId holds the workspace's volume, so it stays set while stopped.
	AgentId *string
	// TokenHash is the git token's, while the workspace is active.
	TokenHash      *string
	TokenExpiresAt *time.Time
	Failure        string // why it last stopped by itself
	// AccessLostAt is when its user was found to have lost access to it.
	AccessLostAt *time.Time
	LastUsedAt   time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Workspace) TableName() string { return "workspace" }

// WorkspaceAgentToken registers workspace agents.
type WorkspaceAgentToken struct {
	Id         string `gorm:"primaryKey"`
	Name       string
	Reusable   bool
	TokenHash  string
	CreatedBy  string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	CreatedAt  time.Time
}

func (WorkspaceAgentToken) TableName() string { return "workspace_agent_token" }

// WorkspaceAgent is an `ogit workspace-agent` on a Docker host.
type WorkspaceAgent struct {
	Id                  string `gorm:"primaryKey"`
	Name                string
	TokenHash           string
	Version             string
	RegistrationTokenId *string
	ProxyURL            string `gorm:"column:proxy_url"`
	ProxyFingerprint    string
	LastSeenAt          time.Time
	CreatedAt           time.Time
}

func (WorkspaceAgent) TableName() string { return "workspace_agent" }

// WorkspaceCleanup is a deleted workspace's container and volume, still on
// its agent.
type WorkspaceCleanup struct {
	WorkspaceId string `gorm:"primaryKey"`
	AgentId     string
	CreatedAt   time.Time
}

func (WorkspaceCleanup) TableName() string { return "workspace_cleanup" }

// WorkspaceTicket is a one-time link that signs a browser in to a workspace's
// host (port 0: the editor).
type WorkspaceTicket struct {
	TokenHash   string `gorm:"primaryKey"`
	WorkspaceId string
	SessionId   string
	Port        int
	ExpiresAt   time.Time
}

func (WorkspaceTicket) TableName() string { return "workspace_ticket" }

type WorkspacePers interface {
	// Create adds a workspace if the user has fewer than maxTotal (0: no
	// limit). False means they don't.
	Create(w *Workspace, maxTotal int) (bool, error)
	// Get returns the user's workspace.
	Get(userId, id string) (*Workspace, error)
	// GetAny returns a workspace, whoever's it is.
	GetAny(id string) (*Workspace, error)
	// List returns the user's workspaces, of one repository unless
	// repositoryId is empty, the most recently used first.
	List(userId, repositoryId string) ([]Workspace, error)
	// All returns every workspace, the most recently used first.
	All() ([]Workspace, error)
	// Start moves a stopped workspace to starting if the user has fewer
	// than max active ones. False means it didn't: check the state.
	Start(userId, id string, max int, now time.Time) (bool, error)
	// Stop takes a starting or running workspace's token away: stopped if no
	// agent has it, stopping otherwise.
	Stop(id string, now time.Time) error
	// Delete removes a stopped workspace, or any with force, leaving a
	// cleanup for its agent. False means it wasn't deleted.
	Delete(id string, force bool) (bool, error)
	// ByTokenHash returns the active workspace with that unexpired token.
	ByTokenHash(hash string, now time.Time) (*Workspace, error)

	CreateAgentToken(t *WorkspaceAgentToken) error
	// AgentTokens lists reusable, unexpired tokens with how many agents each
	// registered.
	AgentTokens(now time.Time) ([]WorkspaceAgentToken, map[string]int64, error)
	RevokeAgentToken(id string) (bool, error)
	// TakeAgentToken returns the valid token with that hash, using it up
	// unless it's reusable.
	TakeAgentToken(hash string, now time.Time) (*WorkspaceAgentToken, error)
	CreateAgent(a *WorkspaceAgent) error
	AgentByTokenHash(hash string) (*WorkspaceAgent, error)
	GetAgent(id string) (*WorkspaceAgent, error)
	Agents() ([]WorkspaceAgent, error)
	// DeleteAgent forgets an agent: its workspaces stop and lose it.
	DeleteAgent(id, failure string, now time.Time) (bool, error)
	TouchAgent(id, version, proxyURL, proxyFingerprint string, now time.Time) error
	CreateTicket(t *WorkspaceTicket) error
	// TakeTicket returns the unexpired ticket with that hash, once.
	TakeTicket(hash string, now time.Time) (*WorkspaceTicket, error)
	DeleteExpiredTickets(now time.Time) error

	// ForAgent returns the agent's starting, running and stopping workspaces.
	ForAgent(agentId string) ([]Workspace, error)
	// MarkRunning moves the agent's starting workspace to running.
	MarkRunning(agentId, id string, now time.Time) error
	// ExtendTokens moves the expiry of the agent's workspaces' tokens.
	ExtendTokens(agentId string, ids []string, expires time.Time) error
	// MarkStopped stops the agent's stopping workspaces, but those in keep.
	MarkStopped(agentId string, keep []string, now time.Time) error
	// Fail stops the agent's active workspace with a reason.
	Fail(agentId, id, failure string, now time.Time) error
	// Claim gives the agent up to n starting workspaces nobody has, each
	// claimed by one conditional update.
	Claim(agentId string, n int, now time.Time) ([]Workspace, error)
	// SetToken gives the agent's starting workspace a new token. False means
	// it isn't one any more.
	SetToken(agentId, id, hash string, expires, now time.Time) (bool, error)
	// Cleanups returns what the agent still has to remove.
	Cleanups(agentId string) ([]string, error)
	AckCleanups(agentId string, ids []string) error

	// FailLost stops active workspaces of agents unseen since before.
	FailLost(before time.Time, failure string, now time.Time) (int64, error)
	// FailStuck stops workspaces starting since before.
	FailStuck(before time.Time, failure string, now time.Time) (int64, error)
	// MarkAccessLost stops the workspace, unless it's already marked. False
	// means it was.
	MarkAccessLost(id, failure string, now time.Time) (bool, error)
	// ClearAccessLost forgets the mark, and the failure if it's that one.
	ClearAccessLost(id, failure string, now time.Time) error
	// DeleteExpiredAgentTokens removes tokens past their expiry.
	DeleteExpiredAgentTokens(now time.Time) error
}
