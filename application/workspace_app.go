package application

import (
	"errors"
	"regexp"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

// Workspace rules:
//   - workspaces are off until workspaces.domain is set; nothing can be
//     created or started then, but existing ones can still be stopped and
//     deleted;
//   - a workspace belongs to one user and one branch (one per user and
//     branch), and only that user sees or manages it: to anyone else it
//     doesn't exist, whatever their role;
//   - creating and starting need write access to the repository (developers
//     and up, checked every time, so losing access stops new starts), on a
//     repository that takes pushes (not archived, not a mirror); readers
//     fork first;
//   - stopping and deleting need no repository access, so a user who lost
//     it can still clean up;
//   - a user has at most workspaces.max_running active workspaces (any state
//     but stopped), counted in one conditional update (TestWorkspaceLimit);
//   - only stopped workspaces can be deleted;
//   - listing a workspace of a repository the user can no longer read hides
//     the repository's path.

var (
	ErrWorkspaceNotFound  = errors.New("workspace not found")
	ErrWorkspacesDisabled = errors.New("workspaces are off on this server: set workspaces.domain to turn them on")
	ErrWorkspaceExists    = domain.ErrWorkspaceExists
	ErrWorkspaceBranch    = errors.New("that branch doesn't exist")
	ErrWorkspaceLimit     = errors.New("you have too many workspaces running; stop one first")
	ErrWorkspaceActive    = errors.New("stop the workspace first")
)

// Ids reach Postgres as UUIDs, which refuses anything else.
var workspaceId = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type WorkspaceApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Workspaces   domain.WorkspacePers
}

// WorkspaceView is a workspace with its repository's path, empty when the
// user can't read the repository any more.
type WorkspaceView struct {
	domain.Workspace
	Repository string
}

func (c *WorkspaceApp) enabled() error {
	if c.Config.Workspaces.Domain == "" {
		return ErrWorkspacesDisabled
	}
	return nil
}

// writable returns the repository if the user may work on it.
func (c *WorkspaceApp) writable(id, path *string, userId string) (*domain.Repository, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, err
	}
	role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
	if err != nil {
		return nil, err
	}
	if !role.CanWrite() {
		return nil, ErrForbidden
	}
	if repo.IsArchived {
		return nil, ErrGitArchived
	}
	if repo.IsMirror {
		return nil, ErrGitMirror
	}
	return repo, nil
}

func (c *WorkspaceApp) view(w domain.Workspace, userId string) WorkspaceView {
	view := WorkspaceView{Workspace: w}
	if repo, err := c.Repositories.GetRepositoryForUser(&w.RepositoryId, nil, userId); err == nil {
		view.Repository = repo.FullPath()
	}
	return view
}

func (c *WorkspaceApp) get(userId, id string) (*domain.Workspace, error) {
	if !workspaceId.MatchString(id) {
		return nil, ErrWorkspaceNotFound
	}
	w, err := c.Workspaces.Get(userId, id)
	if isNotFound(err) {
		return nil, ErrWorkspaceNotFound
	}
	return w, err
}

// Create makes a stopped workspace on branch (the default branch when empty).
func (c *WorkspaceApp) Create(id, path *string, userId, branch string) (*WorkspaceView, error) {
	if err := c.enabled(); err != nil {
		return nil, err
	}
	repo, err := c.writable(id, path, userId)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, err
	}
	if _, err := branchTip(st, branch); err != nil {
		return nil, ErrWorkspaceBranch
	}
	w := &domain.Workspace{RepositoryId: repo.Id, UserId: userId, Branch: branch, State: domain.WorkspaceStopped, LastUsedAt: time.Now().UTC()}
	if err := c.Workspaces.Create(w); err != nil {
		return nil, err
	}
	view := WorkspaceView{Workspace: *w, Repository: repo.FullPath()}
	return &view, nil
}

// ListForRepository returns the user's workspaces on a repository they read.
func (c *WorkspaceApp) ListForRepository(id, path *string, userId string) ([]WorkspaceView, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, err
	}
	return c.list(userId, repo.Id)
}

// List returns all the user's workspaces.
func (c *WorkspaceApp) List(userId string) ([]WorkspaceView, error) {
	return c.list(userId, "")
}

func (c *WorkspaceApp) list(userId, repositoryId string) ([]WorkspaceView, error) {
	workspaces, err := c.Workspaces.List(userId, repositoryId)
	if err != nil {
		return nil, err
	}
	views := make([]WorkspaceView, 0, len(workspaces))
	for _, w := range workspaces {
		views = append(views, c.view(w, userId))
	}
	return views, nil
}

func (c *WorkspaceApp) Get(userId, id string) (*WorkspaceView, error) {
	w, err := c.get(userId, id)
	if err != nil {
		return nil, err
	}
	view := c.view(*w, userId)
	return &view, nil
}

// Start asks for the workspace to run; starting one that's active is fine.
func (c *WorkspaceApp) Start(userId, id string) (*WorkspaceView, error) {
	if err := c.enabled(); err != nil {
		return nil, err
	}
	w, err := c.get(userId, id)
	if err != nil {
		return nil, err
	}
	if _, err := c.writable(&w.RepositoryId, nil, userId); err != nil {
		return nil, err
	}
	if w.State != domain.WorkspaceStopped {
		return c.Get(userId, id)
	}
	max := c.Config.Workspaces.MaxRunning
	if max < 1 {
		max = 1
	}
	started, err := c.Workspaces.Start(userId, id, max, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	view, err := c.Get(userId, id)
	if err != nil {
		return nil, err
	}
	if !started && view.State == domain.WorkspaceStopped {
		return nil, ErrWorkspaceLimit
	}
	return view, nil
}

// Stop stops the workspace; stopping a stopped one is fine.
func (c *WorkspaceApp) Stop(userId, id string) (*WorkspaceView, error) {
	if _, err := c.get(userId, id); err != nil {
		return nil, err
	}
	if err := c.Workspaces.Stop(userId, id, time.Now().UTC()); err != nil {
		return nil, err
	}
	return c.Get(userId, id)
}

func (c *WorkspaceApp) Delete(userId, id string) error {
	if _, err := c.get(userId, id); err != nil {
		return err
	}
	deleted, err := c.Workspaces.Delete(userId, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrWorkspaceActive
	}
	return nil
}
