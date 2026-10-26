package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

// Actions rules:
//   - workflows (GitHub Actions syntax) live in .ogit/workflows, else
//     .github/workflows; a push runs those whose `on: push` filters
//     (branches, tags and their -ignore lists) match the pushed ref;
//   - a run is one workflow for one event; one runner takes it whole and
//     runs it with act (jobs, needs, matrix), in Docker;
//   - runners register with a one-time registration token (an hour) from
//     their scope: site admins (every repository), group owners (the
//     group's repositories) or repository maintainers (that repository).
//     A runner takes queued runs from its scope whose runs-on labels it
//     has, oldest first; a run is claimed once (conditional UPDATE);
//   - each run gets a job token (GITHUB_TOKEN), valid while it runs: it
//     clones its repository (read only for now);
//   - a running run whose runner stops reporting for 10 minutes fails;
//   - writers cancel runs; readers see runs, jobs and logs.

var (
	ErrRunnerNotFound    = errors.New("runner not found")
	ErrRunNotFound       = errors.New("run not found")
	ErrRegistrationToken = errors.New("invalid or expired registration token")
	ErrRunnerToken       = errors.New("invalid runner token")
	ErrRunNotRunning     = errors.New("the run is not running on this runner")
	ErrRunnerScope       = errors.New("scope must be site, group or repository")
)

const (
	registrationTokenLifetime = time.Hour
	runnerHeartbeatTimeout    = 10 * time.Minute
	maxLogChunk               = 1 << 20
	jobTokenPrefix            = "ogitj_"
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
	Now          func() time.Time
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
	if c == nil {
		return
	}
	logger := c.Logger.With().Str("component", "application.actions").Str("repository", repo.FullPath()).Logger()
	for _, u := range updates {
		if u.New.IsZero() || !(u.Name.IsBranch() || u.Name.IsTag()) {
			continue
		}
		files, err := ReadWorkflows(objects, u.New)
		if err != nil {
			logger.Warn().Err(err).Str("ref", u.Name.String()).Msg("failed to read workflows")
			continue
		}
		for _, f := range files {
			if f.Parsed == nil || !f.Parsed.MatchesPush(u.Name) {
				continue
			}
			payload, _ := json.Marshal(c.Webhooks.PushPayloadFor(repo, pusher, objects, u))
			if err := c.queue(repo, f, "push", string(payload), u.Name.String(), u.New, pusher.UserId, pusher.Name); err != nil {
				logger.Error().Err(err).Str("workflow", f.Path).Msg("failed to queue a run")
			}
		}
	}
}

func (c *ActionsApp) queue(repo *domain.Repository, f WorkflowFile, event, payload, ref string, sha plumbing.Hash, actorId, actorName string) error {
	run := &domain.ActionRun{
		RepositoryId: repo.Id, WorkflowPath: f.Path, WorkflowName: f.Parsed.Name, Workflow: f.Content,
		Event: event, EventPayload: payload, Ref: ref, Sha: sha.String(), ActorName: actorName,
		Labels: strings.Join(f.Parsed.Labels, ","), Status: domain.RunQueued, CreatedAt: c.now(),
	}
	if run.WorkflowName == "" {
		run.WorkflowName = f.Path
	}
	if domain.IsPerson(actorId) {
		run.ActorId = &actorId
	}
	return c.Pers.CreateRun(run)
}

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

// RegistrationToken creates a one-time token that registers a runner in
// scope.
func (c *ActionsApp) RegistrationToken(actorId string, scope RunnerScope) (string, time.Time, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return "", time.Time{}, err
	}
	raw := randomToken("ogitreg_")
	t := &domain.ActionRegistrationToken{ScopeType: scope.Type, ScopeId: scope.Id, TokenHash: HashToken(raw), CreatedBy: actorId,
		ExpiresAt: c.now().Add(registrationTokenLifetime), CreatedAt: c.now()}
	if err := c.Pers.CreateRegistrationToken(t); err != nil {
		return "", time.Time{}, err
	}
	return raw, t.ExpiresAt, nil
}

// Register turns a registration token into a runner and its token.
func (c *ActionsApp) Register(registrationToken, name string, labels []string, version string) (*domain.ActionRunner, string, error) {
	t, err := c.Pers.TakeRegistrationToken(HashToken(registrationToken), c.now())
	if err != nil {
		return nil, "", ErrRegistrationToken
	}
	raw := randomToken("ogitr_")
	now := c.now()
	r := &domain.ActionRunner{Name: strings.TrimSpace(name), ScopeType: t.ScopeType, ScopeId: t.ScopeId, Labels: cleanLabels(labels),
		TokenHash: HashToken(raw), Version: version, LastSeenAt: &now, CreatedAt: now}
	if r.Name == "" {
		r.Name = "runner"
	}
	if err := c.Pers.CreateRunner(r); err != nil {
		return nil, "", err
	}
	c.Logger.Info().Str("component", "audit").Str("runner_id", r.Id).Str("scope", r.ScopeType).Msg("runner registered")
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
	if err != nil || r.ScopeType != scope.Type || (scope.Id != nil && (r.ScopeId == nil || *r.ScopeId != *scope.Id)) {
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
}

// scopeRepositories returns the repositories a runner serves (nil: all).
func (c *ActionsApp) scopeRepositories(r *domain.ActionRunner) ([]string, error) {
	switch r.ScopeType {
	case domain.RunnerScopeSite:
		return nil, nil
	case domain.RunnerScopeRepository:
		return []string{*r.ScopeId}, nil
	case domain.RunnerScopeGroup:
		repos, err := c.Repositories.RepoPres.InNamespace(domain.OwnerGroup, *r.ScopeId)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(repos))
		for _, repo := range repos {
			ids = append(ids, repo.Id)
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
	_ = c.Pers.TouchRunner(r.Id, r.Labels, version, c.now())
	ids, err := c.scopeRepositories(r)
	if err != nil {
		return nil, err
	}
	runs, err := c.Pers.QueuedRuns(ids, 50)
	if err != nil {
		return nil, err
	}
	have := splitLabels(r.Labels)
	for i := range runs {
		run := &runs[i]
		if !hasLabels(have, splitLabels(run.Labels)) {
			continue
		}
		token := randomToken(jobTokenPrefix)
		ok, err := c.Pers.ClaimRun(run.Id, r.Id, HashToken(token), c.now())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // another runner took it
		}
		repo, err := c.Repositories.GetRepository(&run.RepositoryId, nil)
		if err != nil {
			return nil, err
		}
		payload := json.RawMessage(run.EventPayload)
		if !json.Valid(payload) {
			payload = json.RawMessage("{}")
		}
		return &Task{RunId: run.Id, Number: run.Number, Repository: repo.FullPath(), DefaultBranch: repo.DefaultBranch,
			WorkflowPath: run.WorkflowPath, Workflow: run.Workflow, Event: run.Event, EventPayload: payload,
			Ref: run.Ref, Sha: run.Sha, Actor: run.ActorName, Token: token, ServerURL: serverURL,
			Secrets: map[string]string{}, Vars: map[string]string{}}, nil
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
	return run.CancelAsked, nil
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

// --- runs ------------------------------------------------------------------

// Runs lists a repository's runs for a reader.
func (c *ActionsApp) Runs(id, path *string, readerId string, offset, limit int) (*domain.Repository, []domain.ActionRun, int64, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, readerId)
	if err != nil {
		return nil, nil, 0, err
	}
	runs, total, err := c.Pers.ListRuns(repo.Id, max(offset, 0), clampLimit(limit))
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
		}
	}
	return len(runs)
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
			c.FailStale()
		}
	}
}
