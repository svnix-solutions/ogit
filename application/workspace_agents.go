package application

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
)

// Workspace agent rules:
//   - only admins manage agents, and their registration tokens (one-time for
//     an hour, or reusable until they expire or are revoked; revoking keeps
//     the agents). The server never touches Docker: agents do;
//   - an agent only reports on workspaces it holds: every update is
//     conditional on agent_id;
//   - a starting workspace goes to the agent that holds its volume, or else
//     to the first with a free slot (one conditional update, so to one);
//   - a sync is a full report, so a lost answer repeats: starts get a new
//     token, and a container nothing wants is stopped whatever the reason;
//   - a workspace's git token (ogitw_, stored hashed) exists only while the
//     workspace starts or runs: stopping clears it at once. It's good for
//     workspaceTokenLifetime and only a sync reporting the container
//     running extends it, so it dies with its agent;
//   - agents unseen for agentLostAfter lose their active workspaces (stopped,
//     with the reason), and workspaces that don't start in
//     workspaceStartTimeout stop too, so they stop counting against limits.

var (
	ErrAgentRegistration      = errors.New("invalid or expired registration token")
	ErrAgentToken             = errors.New("invalid agent token")
	ErrInvalidAgentToken      = errors.New("a name is at most 100 characters; only reusable tokens take an expiry, which must be in the future")
	ErrAgentTokenNotFound     = errors.New("registration token not found")
	ErrWorkspaceAgentNotFound = errors.New("workspace agent not found")
)

const (
	workspaceTokenPrefix   = "ogitw_"
	workspaceTokenLifetime = time.Hour
	agentTokenLifetime     = time.Hour
	agentLostAfter         = 5 * time.Minute
	workspaceStartTimeout  = 30 * time.Minute
	defaultRetention       = 14 * 24 * time.Hour
	maxWorkspaceFailure    = 500
	failureAgentLost       = "its agent stopped reporting"
	failureAgentDeleted    = "its agent was deleted"
	failureNotStarted      = "no agent started it in time"
	failureRepositoryGone  = "its repository is gone"
	failureAccessLost      = "its user lost access"
)

// IsWorkspaceToken reports whether a git password is a workspace's token.
func IsWorkspaceToken(token string) bool { return strings.HasPrefix(token, workspaceTokenPrefix) }

// TokenWorkspace returns the active workspace of an unexpired token.
func (c *WorkspaceApp) TokenWorkspace(token string) (*domain.Workspace, error) {
	return c.Workspaces.ByTokenHash(HashToken(token), time.Now().UTC())
}

// AgentTokenOptions: a one-time token by default.
type AgentTokenOptions struct {
	Reusable  bool
	Name      string
	ExpiresAt *time.Time // reusable tokens only; nil: never
}

// AgentToken creates a token that registers workspace agents.
func (c *WorkspaceApp) AgentToken(actorId string, o AgentTokenOptions) (*domain.WorkspaceAgentToken, string, error) {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return nil, "", err
	}
	now := time.Now().UTC()
	name := strings.TrimSpace(o.Name)
	if len(name) > 100 || (o.ExpiresAt != nil && (!o.Reusable || !o.ExpiresAt.After(now))) {
		return nil, "", ErrInvalidAgentToken
	}
	expires := o.ExpiresAt
	if expires != nil {
		e := expires.UTC()
		expires = &e
	}
	if !o.Reusable {
		e := now.Add(agentTokenLifetime)
		expires = &e
	}
	raw := randomToken("ogitwreg_")
	t := &domain.WorkspaceAgentToken{Name: name, Reusable: o.Reusable, TokenHash: HashToken(raw), CreatedBy: actorId, ExpiresAt: expires, CreatedAt: now}
	if err := c.Workspaces.CreateAgentToken(t); err != nil {
		return nil, "", err
	}
	c.Logger.Info().Str("component", "audit").Str("agent_token_id", t.Id).Bool("reusable", t.Reusable).Str("by", actorId).Msg("workspace agent registration token created")
	return t, raw, nil
}

// AgentTokens lists reusable tokens, with how many agents each registered.
func (c *WorkspaceApp) AgentTokens(actorId string) ([]domain.WorkspaceAgentToken, map[string]int64, error) {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return nil, nil, err
	}
	return c.Workspaces.AgentTokens(time.Now().UTC())
}

func (c *WorkspaceApp) RevokeAgentToken(actorId, id string) error {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return err
	}
	if !workspaceId.MatchString(id) {
		return ErrAgentTokenNotFound
	}
	revoked, err := c.Workspaces.RevokeAgentToken(id)
	if err != nil {
		return err
	}
	if !revoked {
		return ErrAgentTokenNotFound
	}
	c.Logger.Info().Str("component", "audit").Str("agent_token_id", id).Str("by", actorId).Msg("workspace agent registration token revoked")
	return nil
}

// Agents lists the agents.
func (c *WorkspaceApp) Agents(actorId string) ([]domain.WorkspaceAgent, error) {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	return c.Workspaces.Agents()
}

// DeleteAgent forgets an agent, and stops its token working. Its workspaces
// stop here and start afresh elsewhere; containers left on the host are the
// admin's to remove.
func (c *WorkspaceApp) DeleteAgent(actorId, id string) error {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return err
	}
	if !workspaceId.MatchString(id) {
		return ErrWorkspaceAgentNotFound
	}
	deleted, err := c.Workspaces.DeleteAgent(id, failureAgentDeleted, time.Now().UTC())
	if err != nil {
		return err
	}
	if !deleted {
		return ErrWorkspaceAgentNotFound
	}
	c.Logger.Info().Str("component", "audit").Str("agent_id", id).Str("by", actorId).Msg("workspace agent deleted")
	return nil
}

// AdminList returns every workspace, with its repository's path.
func (c *WorkspaceApp) AdminList(actorId string) ([]WorkspaceView, error) {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return nil, err
	}
	workspaces, err := c.Workspaces.All()
	if err != nil {
		return nil, err
	}
	views := make([]WorkspaceView, 0, len(workspaces))
	for _, w := range workspaces {
		view := WorkspaceView{Workspace: w}
		if repo, err := c.Repositories.GetRepository(&w.RepositoryId, nil); err == nil {
			view.Repository = repo.FullPath()
		}
		views = append(views, view)
	}
	return views, nil
}

func (c *WorkspaceApp) getAny(id string) (*domain.Workspace, error) {
	if !workspaceId.MatchString(id) {
		return nil, ErrWorkspaceNotFound
	}
	w, err := c.Workspaces.GetAny(id)
	if isNotFound(err) {
		return nil, ErrWorkspaceNotFound
	}
	return w, err
}

// AdminStop stops anyone's workspace.
func (c *WorkspaceApp) AdminStop(actorId, id string) error {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return err
	}
	w, err := c.getAny(id)
	if err != nil {
		return err
	}
	if err := c.Workspaces.Stop(id, time.Now().UTC()); err != nil {
		return err
	}
	c.Logger.Info().Str("component", "audit").Str("workspace_id", id).Str("user_id", w.UserId).Str("by", actorId).Msg("workspace stopped by an admin")
	return nil
}

// AdminDelete deletes anyone's workspace, even a running one.
func (c *WorkspaceApp) AdminDelete(actorId, id string) error {
	if err := c.Admin.RequireAdmin(actorId); err != nil {
		return err
	}
	w, err := c.getAny(id)
	if err != nil {
		return err
	}
	if _, err := c.Workspaces.Delete(id, true); err != nil {
		return err
	}
	c.Logger.Info().Str("component", "audit").Str("workspace_id", id).Str("user_id", w.UserId).Str("by", actorId).Msg("workspace deleted by an admin")
	return nil
}

// --- the agent protocol ------------------------------------------------------

// RegisterAgent turns a registration token into an agent and its token.
func (c *WorkspaceApp) RegisterAgent(registrationToken, name, version string) (*domain.WorkspaceAgent, string, error) {
	now := time.Now().UTC()
	t, err := c.Workspaces.TakeAgentToken(HashToken(registrationToken), now)
	if err != nil {
		return nil, "", ErrAgentRegistration
	}
	raw := randomToken("ogita_")
	a := &domain.WorkspaceAgent{Name: strings.TrimSpace(name), TokenHash: HashToken(raw), Version: version, LastSeenAt: now, CreatedAt: now}
	if len(a.Name) > 100 {
		a.Name = a.Name[:100]
	}
	if t.Reusable {
		a.RegistrationTokenId = &t.Id
	}
	if err := c.Workspaces.CreateAgent(a); err != nil {
		return nil, "", err
	}
	c.Logger.Info().Str("component", "audit").Str("agent_id", a.Id).Str("name", a.Name).Msg("workspace agent registered")
	return a, raw, nil
}

// AgentOnline reports whether an agent has been heard from lately.
func AgentOnline(a domain.WorkspaceAgent) bool { return time.Since(a.LastSeenAt) <= agentLostAfter }

// AuthenticateAgent finds the agent by its token.
func (c *WorkspaceApp) AuthenticateAgent(token string) (*domain.WorkspaceAgent, error) {
	if !strings.HasPrefix(token, "ogita_") {
		return nil, ErrAgentToken
	}
	a, err := c.Workspaces.AgentByTokenHash(HashToken(token))
	if err != nil {
		return nil, ErrAgentToken
	}
	return a, nil
}

// AgentFailure is a workspace that couldn't go on.
type AgentFailure struct {
	Id      string `json:"id"`
	Message string `json:"message"`
}

// AgentReport is what an agent knows. Running are its running containers,
// Busy those it's starting or stopping, Slots how many more it can take.
type AgentReport struct {
	Version string         `json:"version"`
	Running []string       `json:"running"`
	Busy    []string       `json:"busy"`
	Failed  []AgentFailure `json:"failed"`
	Removed []string       `json:"removed"`
	Slots   int            `json:"slots"`
	// ProxyURL is where the server reaches this agent, and ProxyFingerprint
	// the SHA-256 of its certificate; both empty: no browser access.
	ProxyURL         string `json:"proxy_url"`
	ProxyFingerprint string `json:"proxy_fingerprint"`
	// Wait: seconds to wait for work (at most 30).
	Wait int `json:"wait"`
}

// WorkspaceTask starts a workspace. Repository is its path with .git, for
// the agent to put after the server's address.
type WorkspaceTask struct {
	Id         string `json:"id"`
	Image      string `json:"image"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Token      string `json:"token"`
	CPUs       int    `json:"cpus"`
	MemoryGB   int    `json:"memory_gb"`
	DiskGB     int    `json:"disk_gb"`
	Pids       int    `json:"pids"`
}

// AgentTasks is what an agent has to do.
type AgentTasks struct {
	Start  []WorkspaceTask `json:"start"`
	Stop   []string        `json:"stop"`
	Remove []string        `json:"remove"`
	// ProxyKey checks that requests to the agent's listener are the server's.
	ProxyKey string `json:"proxy_key"`
}

// Empty reports whether there's nothing to do.
func (t *AgentTasks) Empty() bool {
	return len(t.Start) == 0 && len(t.Stop) == 0 && len(t.Remove) == 0
}

func validIds(ids []string) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !workspaceId.MatchString(id) })
}

var agentFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// validAgentProxy: an https address and a certificate to pin, or nothing.
func validAgentProxy(address, fingerprint string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && agentFingerprint.MatchString(fingerprint)
}

// Report records what the agent says. Ids are checked: they reach Postgres.
func (c *WorkspaceApp) Report(a *domain.WorkspaceAgent, r AgentReport) error {
	now := time.Now().UTC()
	if !validAgentProxy(r.ProxyURL, r.ProxyFingerprint) {
		r.ProxyURL, r.ProxyFingerprint = "", ""
	}
	if err := c.Workspaces.TouchAgent(a.Id, r.Version, r.ProxyURL, r.ProxyFingerprint, now); err != nil {
		return err
	}
	if err := c.Workspaces.AckCleanups(a.Id, validIds(r.Removed)); err != nil {
		return err
	}
	for _, f := range r.Failed {
		if !workspaceId.MatchString(f.Id) {
			continue
		}
		message := f.Message
		if len(message) > maxWorkspaceFailure {
			message = message[:maxWorkspaceFailure]
		}
		if err := c.Workspaces.Fail(a.Id, f.Id, message, now); err != nil {
			return err
		}
	}
	running := validIds(r.Running)
	for _, id := range running {
		if err := c.Workspaces.MarkRunning(a.Id, id, now); err != nil {
			return err
		}
	}
	if err := c.Workspaces.ExtendTokens(a.Id, running, now.Add(workspaceTokenLifetime)); err != nil {
		return err
	}
	return c.Workspaces.MarkStopped(a.Id, append(running, validIds(r.Busy)...), now)
}

// Tasks answers what the agent should do, given its report.
func (c *WorkspaceApp) Tasks(a *domain.WorkspaceAgent, r AgentReport) (*AgentTasks, error) {
	now := time.Now().UTC()
	mine, err := c.Workspaces.ForAgent(a.Id)
	if err != nil {
		return nil, err
	}
	tasks := &AgentTasks{ProxyKey: c.proxyKey(a.Id)}
	if tasks.Remove, err = c.Workspaces.Cleanups(a.Id); err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	var start []domain.Workspace
	for _, w := range mine {
		if w.State == domain.WorkspaceStarting || w.State == domain.WorkspaceRunning {
			wanted[w.Id] = true
		}
		if w.State == domain.WorkspaceStarting && !slices.Contains(r.Running, w.Id) && !slices.Contains(r.Busy, w.Id) {
			start = append(start, w)
		}
	}
	for _, id := range r.Running {
		if !wanted[id] && !slices.Contains(tasks.Remove, id) {
			tasks.Stop = append(tasks.Stop, id)
		}
	}
	claimed, err := c.Workspaces.Claim(a.Id, r.Slots-len(start), now)
	if err != nil {
		return nil, err
	}
	for _, w := range append(start, claimed...) {
		task, err := c.task(a, w, now)
		if err != nil {
			return nil, err
		}
		if task != nil {
			tasks.Start = append(tasks.Start, *task)
		}
	}
	return tasks, nil
}

// task gives the workspace a new token: only the agent's answer holds it.
func (c *WorkspaceApp) task(a *domain.WorkspaceAgent, w domain.Workspace, now time.Time) (*WorkspaceTask, error) {
	repo, err := c.Repositories.GetRepository(&w.RepositoryId, nil)
	if isNotFound(err) || errors.Is(err, ErrRepositoryNotFound) {
		return nil, c.Workspaces.Fail(a.Id, w.Id, failureRepositoryGone, now)
	} else if err != nil {
		return nil, err
	}
	raw := randomToken(workspaceTokenPrefix)
	set, err := c.Workspaces.SetToken(a.Id, w.Id, HashToken(raw), now.Add(workspaceTokenLifetime), now)
	if err != nil || !set {
		return nil, err
	}
	cfg := c.Config.Workspaces
	return &WorkspaceTask{Id: w.Id, Image: cfg.Image, Repository: repo.GitPath() + ".git", Branch: w.Branch, Token: raw,
		CPUs: cfg.CPUs, MemoryGB: cfg.MemoryGB, DiskGB: cfg.DiskGB, Pids: cfg.Pids}, nil
}

// --- the sweep ---------------------------------------------------------------

// Sweep stops workspaces on lost agents, those that never started and those
// whose user can't have them any more, deleting these a retention period
// later. A failing check changes nothing: only a definite answer does.
func (c *WorkspaceApp) Sweep() {
	logger := c.Logger.With().Str("component", "application.workspaces").Logger()
	now := time.Now().UTC()
	if n, err := c.Workspaces.FailLost(now.Add(-agentLostAfter), failureAgentLost, now); err != nil {
		logger.Warn().Err(err).Msg("failed to stop workspaces of lost agents")
	} else if n > 0 {
		logger.Warn().Int64("workspaces", n).Msg("stopped workspaces of lost agents")
	}
	if _, err := c.Workspaces.FailStuck(now.Add(-workspaceStartTimeout), failureNotStarted, now); err != nil {
		logger.Warn().Err(err).Msg("failed to stop workspaces that never started")
	}
	if err := c.Workspaces.DeleteExpiredAgentTokens(now); err != nil {
		logger.Warn().Err(err).Msg("failed to delete expired agent tokens")
	}
	if err := c.Workspaces.DeleteExpiredTickets(now); err != nil {
		logger.Warn().Err(err).Msg("failed to delete expired workspace links")
	}
	workspaces, err := c.Workspaces.All()
	if err != nil {
		logger.Warn().Err(err).Msg("failed to list workspaces")
		return
	}
	for _, w := range workspaces {
		allowed, err := c.mayKeep(w)
		if err != nil {
			logger.Warn().Err(err).Str("workspace_id", w.Id).Msg("failed to check a workspace's access")
			continue
		}
		if err := c.sweepAccess(w, allowed, now); err != nil {
			logger.Warn().Err(err).Str("workspace_id", w.Id).Msg("failed to act on a workspace's access")
		}
	}
}

// sweepAccess applies the rule on lost access to one workspace.
func (c *WorkspaceApp) sweepAccess(w domain.Workspace, allowed bool, now time.Time) error {
	audit := func(msg string) {
		c.Logger.Info().Str("component", "audit").Str("workspace_id", w.Id).Str("user_id", w.UserId).Msg(msg)
	}
	if allowed {
		if w.AccessLostAt == nil {
			return nil
		}
		if err := c.Workspaces.ClearAccessLost(w.Id, failureAccessLost, now); err != nil {
			return err
		}
		audit("workspace kept: its user got access back")
		return nil
	}
	if w.AccessLostAt == nil {
		marked, err := c.Workspaces.MarkAccessLost(w.Id, failureAccessLost, now)
		if err == nil && marked {
			audit("workspace stopped: its user lost access")
		}
		return err
	}
	retention := c.Config.Workspaces.Retention
	if retention <= 0 {
		retention = defaultRetention
	}
	if now.Sub(*w.AccessLostAt) < retention {
		return c.Workspaces.Stop(w.Id, now) // started just before the access went
	}
	if _, err := c.Workspaces.Delete(w.Id, true); err != nil {
		return err
	}
	audit("workspace deleted: its user lost access")
	return nil
}

func (c *WorkspaceApp) mayKeep(w domain.Workspace) (bool, error) {
	user, err := c.Users.GetById(w.UserId)
	if isNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if !user.Active {
		return false, nil
	}
	_, err = c.Repositories.GetRepositoryForUser(&w.RepositoryId, nil, w.UserId)
	if errors.Is(err, ErrRepositoryNotFound) || isNotFound(err) {
		return false, nil
	}
	return err == nil, err
}

// Watch sweeps until ctx ends.
func (c *WorkspaceApp) Watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Sweep()
		}
	}
}
