package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/rs/zerolog"
)

// Actions rules:
//   - workflows (GitHub Actions syntax) live in .ogit/workflows, else
//     .github/workflows; a push runs those whose `on: push` filters
//     (branches, tags and their -ignore lists) match the pushed ref;
//   - a run is one workflow for one event; one runner takes it whole and
//     runs it with act (jobs, needs, matrix), in Docker;
//   - runners register with a registration token from their scope: site
//     admins (every repository), group owners (the group's repositories)
//     or repository maintainers (that repository). A token works once,
//     within an hour, unless it is reusable: then it registers any number
//     of runners until it expires (optional) or is revoked; revoking keeps
//     the runners it registered. A runner takes queued runs from its scope
//     whose runs-on labels it has, oldest first; a run is claimed once
//     (conditional UPDATE);
//   - an ephemeral runner takes one run (SpendRunner, a conditional
//     UPDATE, before the claim) and is deleted when that run ends; if the
//     run goes back to the queue (never acknowledged), it may take another;
//     runners that are ephemeral or registered with a reusable token are
//     deleted after a day unseen (never while running a run); others stay,
//     shown offline;
//   - each run gets a job token (GITHUB_TOKEN), valid while it runs, for
//     its repository only: it reads it, pushes with `permissions:
//     contents: write` and posts statuses with `statuses: write` (never
//     from forks); its pushes start no workflows;
//   - a running run whose runner stops reporting for 10 minutes fails; one
//     whose runner never reported at all goes back to the queue after 2
//     minutes (the answer to its fetch was lost);
//   - writers cancel runs; readers see runs, jobs and logs.

var (
	ErrRunnerNotFound            = errors.New("runner not found")
	ErrRunNotFound               = errors.New("run not found")
	ErrRegistrationToken         = errors.New("invalid or expired registration token")
	ErrRunnerToken               = errors.New("invalid runner token")
	ErrRunNotRunning             = errors.New("the run is not running on this runner")
	ErrRunnerScope               = errors.New("scope must be site, group or repository")
	ErrRunNotDone                = errors.New("the run hasn't finished")
	ErrInvalidRegistration       = errors.New("a registration token's name is at most 100 characters, and only reusable tokens take an expiry, in the future")
	ErrRegistrationTokenNotFound = errors.New("registration token not found")
)

const (
	registrationTokenLifetime = time.Hour
	// pooledRunnerTimeout: runners that are ephemeral or registered with a
	// reusable token are deleted once unseen for this long.
	pooledRunnerTimeout    = 24 * time.Hour
	runnerHeartbeatTimeout = 10 * time.Minute
	maxLogChunk            = 1 << 20
	jobTokenPrefix         = "ogitj_"
)

type ActionsApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.ActionsPers
	Repositories *RepositoryApp
	Groups       domain.GroupPers
	GroupApp     *GroupApp
	Admin        *AdminApp
	Webhooks     *WebhookApp
	// Statuses posts a commit status per job (nil: none).
	Statuses *StatusApp
	// Box seals secrets (nil without security.encryption_key).
	Box *secretbox.Box
	Now func() time.Time
}

func (c *ActionsApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func randomToken(prefix string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// IsJobToken reports whether a git password is a job token.
func IsJobToken(token string) bool { return strings.HasPrefix(token, jobTokenPrefix) }

// --- triggers ---------------------------------------------------------------

// Pushed queues the workflows that a push runs. It never fails the push.
func (c *ActionsApp) Pushed(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer, updates []RefUpdate) {
	if c == nil || pusher.JobToken {
		return // pushes by workflows don't start workflows
	}
	logger := c.Logger.With().Str("component", "application.actions").Str("repository", repo.FullPath()).Logger()
	for _, u := range updates {
		c.refEvent(repo, pusher, u)
		if u.New.IsZero() || !(u.Name.IsBranch() || u.Name.IsTag()) {
			continue
		}
		files, err := ReadWorkflows(objects, u.New)
		if err != nil {
			logger.Warn().Err(err).Str("ref", u.Name.String()).Msg("failed to read workflows")
			continue
		}
		var changed []string
		changedRead := false
		for _, f := range files {
			if f.Parsed == nil || !f.Parsed.MatchesPush(u.Name) {
				continue
			}
			cfg := f.Parsed.Events["push"]
			if fl := filters(cfg); len(fl["paths"]) > 0 || len(fl["paths-ignore"]) > 0 {
				if !changedRead {
					changed, changedRead = changedPaths(objects, u.Old, u.New), true
				}
				if !matchesPaths(cfg, changed) {
					continue
				}
			}
			payload, _ := json.Marshal(c.Webhooks.PushPayloadFor(repo, pusher, objects, u))
			run := c.newRun(repo, f, "push", string(payload), u.Name.String(), u.New, pusher.UserId, pusher.Name)
			if err := c.Pers.CreateRun(run); err != nil {
				logger.Error().Err(err).Str("workflow", f.Path).Msg("failed to queue a run")
			}
		}
		if u.Name.IsBranch() && u.Name.Short() == repo.DefaultBranch {
			c.updateSchedules(repo, files)
		}
	}
}

func joinLabels(labels []string) string { return strings.Join(labels, ",") }

// --- runner scopes and registration -----------------------------------------

// RunnerScope is where runners are managed: the site, a group or a
// repository.
type RunnerScope struct {
	Type string
	Id   *string // group or repository id
}

// GroupScope is a group's runner scope (groups the actor isn't in are not
// found).
func (c *ActionsApp) GroupScope(identifier, actorId string) (RunnerScope, error) {
	g, _, err := c.GroupApp.Get(identifier, actorId)
	if err != nil {
		return RunnerScope{}, err
	}
	return RunnerScope{Type: domain.RunnerScopeGroup, Id: &g.Id}, nil
}

// RepositoryScope is a repository's runner scope (repositories the actor
// can't read are not found).
func (c *ActionsApp) RepositoryScope(id, path *string, actorId string) (RunnerScope, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, actorId)
	if err != nil {
		return RunnerScope{}, err
	}
	return RunnerScope{Type: domain.RunnerScopeRepository, Id: &repo.Id}, nil
}

// CanManage checks that actorId manages runners in scope.
func (c *ActionsApp) CanManage(actorId string, scope RunnerScope) error {
	switch scope.Type {
	case domain.RunnerScopeSite:
		return c.Admin.RequireAdmin(actorId)
	case domain.RunnerScopeGroup:
		g, err := c.Groups.GetById(*scope.Id)
		if err != nil {
			return ErrForbidden
		}
		if g.OwnerId != actorId {
			return ErrForbidden
		}
		return nil
	case domain.RunnerScopeRepository:
		ok, err := c.Repositories.CanUserAccess(*scope.Id, actorId, "admin")
		if err != nil {
			return err
		}
		if !ok {
			return ErrForbidden
		}
		return nil
	}
	return ErrRunnerScope
}

// RegistrationOptions: a one-time token by default.
type RegistrationOptions struct {
	Reusable  bool
	Name      string
	ExpiresAt *time.Time // reusable tokens only; nil: never
}

// RegistrationToken creates a token that registers runners in scope.
func (c *ActionsApp) RegistrationToken(actorId string, scope RunnerScope, o RegistrationOptions) (*domain.ActionRegistrationToken, string, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return nil, "", err
	}
	now := c.now().UTC()
	name := strings.TrimSpace(o.Name)
	if len(name) > 100 || (o.ExpiresAt != nil && (!o.Reusable || !o.ExpiresAt.After(now))) {
		return nil, "", ErrInvalidRegistration
	}
	expires := o.ExpiresAt
	if expires != nil {
		e := expires.UTC()
		expires = &e
	}
	if !o.Reusable {
		e := now.Add(registrationTokenLifetime)
		expires = &e
	}
	raw := randomToken("ogitreg_")
	t := &domain.ActionRegistrationToken{ScopeType: scope.Type, ScopeId: scope.Id, Name: name, Reusable: o.Reusable,
		TokenHash: HashToken(raw), CreatedBy: actorId, ExpiresAt: expires, CreatedAt: now}
	if err := c.Pers.CreateRegistrationToken(t); err != nil {
		return nil, "", err
	}
	if o.Reusable {
		c.Logger.Info().Str("component", "audit").Str("registration_token_id", t.Id).Str("scope", t.ScopeType).Str("by", actorId).Msg("reusable runner registration token created")
	}
	return t, raw, nil
}

// RegistrationTokens lists a scope's reusable registration tokens, with
// how many of the runners each registered are still there.
func (c *ActionsApp) RegistrationTokens(actorId string, scope RunnerScope) ([]domain.ActionRegistrationToken, map[string]int64, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return nil, nil, err
	}
	return c.Pers.ReusableTokens(scope.Type, scope.Id, c.now().UTC())
}

// RevokeRegistrationToken deletes a reusable token of scope; the runners
// it registered stay.
func (c *ActionsApp) RevokeRegistrationToken(actorId string, scope RunnerScope, id string) error {
	if err := c.CanManage(actorId, scope); err != nil {
		return err
	}
	t, err := c.Pers.GetRegistrationToken(id)
	if err != nil || !t.Reusable || !sameScope(t.ScopeType, t.ScopeId, scope) {
		return ErrRegistrationTokenNotFound
	}
	c.Logger.Info().Str("component", "audit").Str("registration_token_id", t.Id).Str("by", actorId).Msg("runner registration token revoked")
	return c.Pers.DeleteRegistrationToken(id)
}

func sameScope(scopeType string, scopeId *string, scope RunnerScope) bool {
	return scopeType == scope.Type && (scope.Id == nil || (scopeId != nil && *scopeId == *scope.Id))
}

// Register turns a registration token into a runner and its token.
func (c *ActionsApp) Register(registrationToken, name string, labels []string, version string, ephemeral bool) (*domain.ActionRunner, string, error) {
	now := c.now().UTC()
	t, err := c.Pers.TakeRegistrationToken(HashToken(registrationToken), now)
	if err != nil {
		return nil, "", ErrRegistrationToken
	}
	raw := randomToken("ogitr_")
	r := &domain.ActionRunner{Name: strings.TrimSpace(name), ScopeType: t.ScopeType, ScopeId: t.ScopeId, Labels: cleanLabels(labels),
		TokenHash: HashToken(raw), Version: version, LastSeenAt: &now, CreatedAt: now, Ephemeral: ephemeral}
	if t.Reusable {
		r.RegistrationTokenId = &t.Id
	}
	if r.Name == "" {
		r.Name = "runner"
	}
	if err := c.Pers.CreateRunner(r); err != nil {
		return nil, "", err
	}
	c.Logger.Info().Str("component", "audit").Str("runner_id", r.Id).Str("scope", r.ScopeType).Bool("ephemeral", ephemeral).Msg("runner registered")
	return r, raw, nil
}

func cleanLabels(labels []string) string {
	var out []string
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func splitLabels(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// Runners lists a scope's runners.
func (c *ActionsApp) Runners(actorId string, scope RunnerScope) ([]domain.ActionRunner, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return nil, err
	}
	return c.Pers.Runners(scope.Type, scope.Id)
}

// DeleteRunner removes a runner of scope (its token stops working).
func (c *ActionsApp) DeleteRunner(actorId string, scope RunnerScope, id string) error {
	if err := c.CanManage(actorId, scope); err != nil {
		return err
	}
	r, err := c.Pers.GetRunner(id)
	if err != nil || !sameScope(r.ScopeType, r.ScopeId, scope) {
		return ErrRunnerNotFound
	}
	c.Logger.Info().Str("component", "audit").Str("runner_id", r.Id).Str("by", actorId).Msg("runner deleted")
	return c.Pers.DeleteRunner(id)
}

// --- the runner protocol ----------------------------------------------------

// AuthenticateRunner finds the runner by its token.
func (c *ActionsApp) AuthenticateRunner(token string) (*domain.ActionRunner, error) {
	if !strings.HasPrefix(token, "ogitr_") {
		return nil, ErrRunnerToken
	}
	r, err := c.Pers.RunnerByTokenHash(HashToken(token))
	if err != nil {
		return nil, ErrRunnerToken
	}
	return r, nil
}

// Task is a run handed to a runner.
type Task struct {
	RunId         string            `json:"run_id"`
	Number        int               `json:"number"`
	Repository    string            `json:"repository"` // owner/name
	DefaultBranch string            `json:"default_branch"`
	WorkflowPath  string            `json:"workflow_path"`
	Workflow      string            `json:"workflow"`
	Event         string            `json:"event"`
	EventPayload  json.RawMessage   `json:"event_payload"`
	Ref           string            `json:"ref"`
	Sha           string            `json:"sha"`
	Actor         string            `json:"actor"`
	Token         string            `json:"token"`
	ServerURL     string            `json:"server_url"`
	Secrets       map[string]string `json:"secrets"`
	Vars          map[string]string `json:"vars"`
	// RunApiId is GITHUB_RUN_ID; RuntimeToken is ACTIONS_RUNTIME_TOKEN
	// (artifacts, at ServerURL).
	RunApiId     int64  `json:"run_api_id"`
	RuntimeToken string `json:"runtime_token"`
}

// scopeRepositories returns the repositories a runner serves (nil: all).
func (c *ActionsApp) scopeRepositories(r *domain.ActionRunner) ([]string, error) {
	switch r.ScopeType {
	case domain.RunnerScopeSite:
		return nil, nil
	case domain.RunnerScopeRepository:
		return []string{*r.ScopeId}, nil
	case domain.RunnerScopeGroup:
		// The group's repositories and its sub-groups'.
		group, err := c.Groups.GetById(*r.ScopeId)
		if err != nil {
			return nil, err
		}
		below, err := c.Groups.Descendants(group.Name)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, g := range append([]domain.Group{*group}, below...) {
			repos, err := c.Repositories.RepoPres.InNamespace(domain.OwnerGroup, g.Id)
			if err != nil {
				return nil, err
			}
			for _, repo := range repos {
				ids = append(ids, repo.Id)
			}
		}
		return ids, nil
	}
	return []string{}, nil
}

// FetchTask claims the oldest queued run the runner can take, if any.
func (c *ActionsApp) FetchTask(r *domain.ActionRunner, labels []string, version, serverURL string) (*Task, error) {
	if labels != nil {
		r.Labels = cleanLabels(labels)
	}
	_ = c.Pers.TouchRunner(r.Id, r.Labels, version, c.now().UTC())
	ids, err := c.scopeRepositories(r)
	if err != nil {
		return nil, err
	}
	runs, err := c.Pers.QueuedRuns(ids, 50)
	if err != nil {
		return nil, err
	}
	have := splitLabels(r.Labels)
	spent := false
	if r.Ephemeral {
		// Spend the runner before claiming, so that concurrent fetches can't
		// both claim a run; give it back if none is claimed.
		defer func() {
			if spent {
				_ = c.Pers.UnspendRunner(r.Id)
			}
		}()
	}
	for i := range runs {
		run := &runs[i]
		if !hasLabels(have, splitLabels(run.Labels)) {
			continue
		}
		if r.Ephemeral && !spent {
			ok, err := c.Pers.SpendRunner(r.Id)
			if err != nil || !ok {
				return nil, err // it has taken its run
			}
			spent = true
		}
		token := randomToken(jobTokenPrefix)
		ok, err := c.Pers.ClaimRun(run.Id, r.Id, HashToken(token), c.now())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // another runner took it
		}
		spent = false // claimed: keep it spent
		repo, err := c.Repositories.GetRepository(&run.RepositoryId, nil)
		if err != nil {
			return nil, err
		}
		payload := json.RawMessage(run.EventPayload)
		if !json.Valid(payload) {
			payload = json.RawMessage("{}")
		}
		secrets, vars, err := c.runSecrets(repo, run)
		if err != nil {
			return nil, err
		}
		return &Task{RunId: run.Id, Number: run.Number, Repository: repo.FullPath(), DefaultBranch: repo.DefaultBranch,
			WorkflowPath: run.WorkflowPath, Workflow: run.Workflow, Event: run.Event, EventPayload: payload,
			Ref: run.Ref, Sha: run.Sha, Actor: run.ActorName, Token: token, ServerURL: serverURL,
			Secrets: secrets, Vars: vars, RunApiId: run.ApiId, RuntimeToken: c.RuntimeToken(run)}, nil
	}
	return nil, nil
}

func hasLabels(have, need []string) bool {
	for _, l := range need {
		if !slices.Contains(have, l) {
			return false
		}
	}
	return true
}

// JobState is a job's state as the runner reports it.
type JobState struct {
	Name   string `json:"name"`
	Status string `json:"status"` // running, success, failure, cancelled, skipped
}

// StateUpdate is what a runner reports about a run it runs.
type StateUpdate struct {
	Jobs []JobState `json:"jobs"`
	// Status: "" while running, else the run's result.
	Status string `json:"status"`
}

// runOf returns the run r is running.
func (c *ActionsApp) runOf(r *domain.ActionRunner, runId string) (*domain.ActionRun, error) {
	run, err := c.Pers.GetRunById(runId)
	if err != nil || run.Status != domain.RunRunning || run.RunnerId == nil || *run.RunnerId != r.Id {
		return nil, ErrRunNotRunning
	}
	return run, nil
}

// UpdateState records a run's progress. cancel: the runner should stop it.
func (c *ActionsApp) UpdateState(r *domain.ActionRunner, runId string, u StateUpdate) (cancel bool, err error) {
	run, err := c.runOf(r, runId)
	if err != nil {
		return false, err
	}
	now := c.now()
	for _, j := range u.Jobs {
		if j.Name == "" {
			continue
		}
		job := &domain.ActionJob{RunId: run.Id, Name: j.Name, Status: j.Status}
		if j.Status == "running" {
			job.StartedAt = &now
		} else {
			job.FinishedAt = &now
		}
		if err := c.Pers.UpsertJob(job); err != nil {
			return false, err
		}
		c.postStatus(run, j.Name, j.Status)
	}
	run.HeartbeatAt = &now
	fields := []string{"heartbeat_at"}
	switch u.Status {
	case "":
	case domain.RunSuccess, domain.RunFailure, domain.RunCancelled:
		run.Status, run.FinishedAt, run.TokenHash = u.Status, &now, ""
		fields = append(fields, "status", "finished_at", "token_hash")
	default:
		return false, errors.New("status must be success, failure or cancelled")
	}
	if err := c.Pers.UpdateRun(run, fields...); err != nil {
		return false, err
	}
	if domain.RunDone(run.Status) {
		c.closeStatuses(run)
		if r.Ephemeral {
			c.Logger.Info().Str("component", "audit").Str("runner_id", r.Id).Msg("ephemeral runner done: deleted")
			if err := c.Pers.DeleteRunner(r.Id); err != nil {
				c.Logger.Error().Err(err).Str("component", "application.actions").Str("runner_id", r.Id).Msg("failed to delete an ephemeral runner")
			}
		}
	}
	return run.CancelAsked, nil
}

// jobStates map a job's status to a commit status and its description.
var jobStates = map[string][2]string{
	"running":   {string(domain.StatusPending), "Running"},
	"success":   {string(domain.StatusSuccess), "Successful"},
	"failure":   {string(domain.StatusFailure), "Failed"},
	"cancelled": {string(domain.StatusError), "Cancelled"},
	"skipped":   {string(domain.StatusSuccess), "Skipped"},
}

// StatusContext is a job's commit status context.
func StatusContext(run *domain.ActionRun, job string) string {
	return run.WorkflowName + " / " + job + " (" + run.Event + ")"
}

// postStatus posts a job's state on the run's commit (a pull request's
// head for pull request runs).
func (c *ActionsApp) postStatus(run *domain.ActionRun, job, status string) {
	state, ok := jobStates[status]
	if c.Statuses == nil || !ok {
		return
	}
	repo, err := c.Repositories.GetRepository(&run.RepositoryId, nil)
	if err != nil {
		return
	}
	sha := run.StatusSha
	if sha == "" {
		sha = run.Sha
	}
	target := ""
	if base := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); base != "" {
		target = base + "/" + repo.FullPath() + "/actions/runs/" + strconv.Itoa(run.Number)
	}
	in := StatusInput{State: state[0], Context: StatusContext(run, job), Description: state[1], TargetURL: target}
	if _, err := c.Statuses.Post(repo, sha, in); err != nil {
		c.Logger.Warn().Err(err).Str("component", "application.actions").Str("run_id", run.Id).Msg("failed to post a job status")
	}
}

// closeStatuses ends the statuses of jobs still running when the run ends
// (cancelled, or its runner went away).
func (c *ActionsApp) closeStatuses(run *domain.ActionRun) {
	jobs, err := c.Pers.Jobs(run.Id)
	if err != nil {
		return
	}
	now := c.now()
	for i := range jobs {
		if jobs[i].Status != "running" {
			continue
		}
		jobs[i].Status, jobs[i].FinishedAt = run.Status, &now
		if jobs[i].Status == domain.RunSuccess {
			jobs[i].Status = domain.RunFailure // a job can't succeed without saying so
		}
		if err := c.Pers.UpsertJob(&jobs[i]); err == nil {
			c.postStatus(run, jobs[i].Name, jobs[i].Status)
		}
	}
}

// AppendLog adds output to a run's log.
func (c *ActionsApp) AppendLog(r *domain.ActionRunner, runId string, data []byte) error {
	run, err := c.runOf(r, runId)
	if err != nil {
		return err
	}
	for len(data) > 0 {
		n := min(len(data), maxLogChunk)
		if err := c.Pers.AppendLog(run.Id, string(data[:n])); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// --- job tokens ------------------------------------------------------------

// JobTokenRun returns the running run of a job token.
func (c *ActionsApp) JobTokenRun(token string) (*domain.ActionRun, error) {
	if c == nil || !IsJobToken(token) {
		return nil, ErrGitUnauthenticated
	}
	run, err := c.Pers.RunByTokenHash(HashToken(token))
	if err != nil {
		return nil, ErrGitUnauthenticated
	}
	return run, nil
}

// hasPermission reports whether a run's token has perm at level (write
// includes read). Runs from forks only read.
func hasPermission(run *domain.ActionRun, perm, level string) bool {
	if run.FromFork && level == "write" {
		return false
	}
	for _, p := range strings.Split(run.Permissions, ",") {
		name, lvl, _ := strings.Cut(p, ":")
		if name == perm && (lvl == level || lvl == "write") {
			return true
		}
	}
	return false
}

// JobToken turns a running job's token into a repository token for its
// repository, with scopes from the workflow's permissions (API routes
// that take repository tokens then accept it).
func (c *ActionsApp) JobToken(token string) (*domain.Token, error) {
	if !IsJobToken(token) {
		return nil, ErrInvalidToken
	}
	return c.JobTokenByHash(HashToken(token))
}

// JobTokenByHash is JobToken for a token known by its hash.
func (c *ActionsApp) JobTokenByHash(hash string) (*domain.Token, error) {
	if c == nil || hash == "" {
		return nil, ErrInvalidToken
	}
	run, err := c.Pers.RunByTokenHash(hash)
	if err != nil {
		return nil, ErrInvalidToken
	}
	scopes := []domain.TokenScope{}
	if hasPermission(run, "contents", "read") {
		scopes = append(scopes, domain.ScopeReadRepository)
	}
	if hasPermission(run, "contents", "write") {
		scopes = append(scopes, domain.ScopeWriteRepository)
	}
	if hasPermission(run, "statuses", "write") {
		scopes = append(scopes, domain.ScopeWriteStatus)
	}
	if hasPermission(run, "packages", "read") {
		scopes = append(scopes, domain.ScopeReadPackages)
	}
	if hasPermission(run, "packages", "write") {
		scopes = append(scopes, domain.ScopeWritePackages)
	}
	repoId := run.RepositoryId
	return &domain.Token{Name: "github-actions", Type: domain.TokenRepo, RepositoryId: &repoId, Active: true, Scopes: scopes}, nil
}

// --- runs ------------------------------------------------------------------

// Runs lists a repository's runs for a reader.
func (c *ActionsApp) Runs(id, path *string, readerId string, f domain.RunFilter, offset, limit int) (*domain.Repository, []domain.ActionRun, int64, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, readerId)
	if err != nil {
		return nil, nil, 0, err
	}
	runs, total, err := c.Pers.ListRuns(repo.Id, f, max(offset, 0), clampLimit(limit))
	return repo, runs, total, err
}

// Run returns a run and its jobs for a reader.
func (c *ActionsApp) Run(id, path *string, readerId string, number int) (*domain.Repository, *domain.ActionRun, []domain.ActionJob, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, readerId)
	if err != nil {
		return nil, nil, nil, err
	}
	run, err := c.Pers.GetRun(repo.Id, number)
	if isNotFound(err) {
		return nil, nil, nil, ErrRunNotFound
	} else if err != nil {
		return nil, nil, nil, err
	}
	jobs, err := c.Pers.Jobs(run.Id)
	return repo, run, jobs, err
}

// Logs returns a run's log from chunk seq on, and the next seq.
func (c *ActionsApp) Logs(id, path *string, readerId string, number, fromSeq int) (string, int, error) {
	_, run, _, err := c.Run(id, path, readerId, number)
	if err != nil {
		return "", 0, err
	}
	chunks, err := c.Pers.Logs(run.Id, max(fromSeq, 1))
	if err != nil {
		return "", 0, err
	}
	var b strings.Builder
	next := max(fromSeq, 1)
	for _, ch := range chunks {
		b.WriteString(ch.Data)
		next = ch.Seq + 1
	}
	return b.String(), next, nil
}

// Cancel stops a run: queued ones at once, running ones when their runner
// next reports.
func (c *ActionsApp) Cancel(id, path *string, actorId string, number int) (*domain.ActionRun, error) {
	repo, run, _, err := c.Run(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, actorId, "write")
	if err != nil {
		return nil, err
	}
	if !ok || !domain.IsPerson(actorId) {
		return nil, ErrForbidden
	}
	now := c.now()
	switch run.Status {
	case domain.RunQueued:
		run.Status, run.FinishedAt = domain.RunCancelled, &now
		return run, c.Pers.UpdateRun(run, "status", "finished_at")
	case domain.RunRunning:
		run.CancelAsked = true
		return run, c.Pers.UpdateRun(run, "cancel_asked")
	}
	return run, nil
}

// Rerun queues a finished run again: same workflow, event and commit
// (writers).
func (c *ActionsApp) Rerun(id, path *string, actorId string, number int) (*domain.ActionRun, error) {
	repo, run, _, err := c.Run(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, actorId, "write")
	if err != nil {
		return nil, err
	}
	if !ok || !domain.IsPerson(actorId) {
		return nil, ErrForbidden
	}
	if !domain.RunDone(run.Status) {
		return nil, ErrRunNotDone
	}
	actor, _ := c.Repositories.UserPers.GetById(actorId)
	again := &domain.ActionRun{
		RepositoryId: run.RepositoryId, WorkflowPath: run.WorkflowPath, WorkflowName: run.WorkflowName, Workflow: run.Workflow,
		Event: run.Event, EventPayload: run.EventPayload, Ref: run.Ref, Sha: run.Sha, ActorId: &actorId, ActorName: actor.Username,
		Labels: run.Labels, Status: domain.RunQueued, Permissions: run.Permissions, FromFork: run.FromFork,
		StatusSha: run.StatusSha, PullNumber: run.PullNumber, CreatedAt: c.now(),
	}
	if err := c.Pers.CreateRun(again); err != nil {
		return nil, err
	}
	return again, nil
}

// LatestRun is the newest finished run of a workflow (by file name or
// path) on a branch (default: the default branch), for badges.
func (c *ActionsApp) LatestRun(repo *domain.Repository, workflow, branch string) (*domain.ActionRun, error) {
	if branch == "" {
		branch = repo.DefaultBranch
	}
	ref := "refs/heads/" + branch
	if !strings.Contains(workflow, "/") {
		for _, dir := range workflowDirs {
			if run, err := c.Pers.LatestRun(repo.Id, dir+"/"+workflow, ref); err == nil {
				return run, nil
			}
		}
		return nil, ErrRunNotFound
	}
	run, err := c.Pers.LatestRun(repo.Id, workflow, ref)
	if err != nil {
		return nil, ErrRunNotFound
	}
	return run, nil
}

// unacknowledgedTimeout: a claimed run whose runner never reported (the
// answer to its fetch was lost, e.g. it was stopped while polling) goes
// back to the queue after this.
const unacknowledgedTimeout = 2 * time.Minute

// Requeue puts back claimed runs whose runner never reported; it returns
// how many.
func (c *ActionsApp) Requeue() int {
	n, err := c.Pers.RequeueUnacknowledged(c.now().Add(-unacknowledgedTimeout))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.actions").Msg("failed to requeue runs")
	}
	return int(n)
}

// FailStale fails running runs whose runner stopped reporting.
func (c *ActionsApp) FailStale() int {
	runs, err := c.Pers.StaleRuns(c.now().Add(-runnerHeartbeatTimeout))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.actions").Msg("failed to list stale runs")
		return 0
	}
	now := c.now()
	for i := range runs {
		run := &runs[i]
		run.Status, run.FinishedAt, run.TokenHash = domain.RunFailure, &now, ""
		if err := c.Pers.UpdateRun(run, "status", "finished_at", "token_hash"); err == nil {
			_ = c.Pers.AppendLog(run.Id, "\nThe runner stopped reporting: the run failed.\n")
			c.closeStatuses(run)
		}
	}
	return len(runs)
}

// SweepRunners deletes ephemeral runners whose run is over (their run
// failed stale or went back to the queue), runners that are ephemeral or
// registered with a reusable token and unseen for a day, and expired
// registration tokens. It returns how many runners it deleted.
func (c *ActionsApp) SweepRunners() int {
	now := c.now().UTC()
	// A spent runner is deleted only once unseen for longer than a fetch
	// takes: it is spent just before its run is claimed.
	n, err := c.Pers.DeleteDoneRunners(now.Add(-unacknowledgedTimeout), now.Add(-pooledRunnerTimeout))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.actions").Msg("failed to delete done runners")
	} else if n > 0 {
		c.Logger.Info().Str("component", "audit").Int64("count", n).Msg("ephemeral and offline runners deleted")
	}
	if _, err := c.Pers.DeleteExpiredRegistrationTokens(now); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.actions").Msg("failed to delete expired registration tokens")
	}
	return int(n)
}

// Run fails stale runs every interval until ctx ends.
func (c *ActionsApp) Watch(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Requeue()
			c.FailStale()
			c.RunSchedules()
			c.SweepRunners()
			c.SweepArtifacts()
			c.SweepCaches()
		}
	}
}
