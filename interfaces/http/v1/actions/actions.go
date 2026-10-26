// Package actions serves the Actions API: runners per scope (site, group,
// repository) and a repository's runs.
package actions

import (
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Actions    *application.ActionsApp
}

type Runner struct {
	Id         string     `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	Labels     []string   `json:"labels"`
	Version    string     `json:"version"`
	Online     bool       `json:"online"` // reported in the last minute
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type RunnersResponse struct {
	Runners []Runner `json:"runners"`
}

type RegistrationTokenResponse struct {
	Token     string    `json:"token"` // shown once
	ExpiresAt time.Time `json:"expires_at"`
}

type SiteRequest struct{}

type SiteRunnerRequest struct {
	RunnerId string `path:"runner_id" json:"-" validate:"required"`
}

type GroupRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type GroupRunnerRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RunnerId   string `path:"runner_id" json:"-" validate:"required"`
}

type RepoRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type RepoRunnerRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RunnerId   string `path:"runner_id" json:"-" validate:"required"`
}

type Run struct {
	Id           string `json:"id"`
	Number       int    `json:"number"`
	WorkflowPath string `json:"workflow_path"`
	WorkflowName string `json:"workflow_name"`
	Event        string `json:"event"`
	Ref          string `json:"ref"`
	Sha          string `json:"sha"`
	Actor        string `json:"actor"`
	// Status: queued, waiting (a fork run awaiting approval), running,
	// success, failure or cancelled.
	Status      string     `json:"status"`
	CancelAsked bool       `json:"cancel_requested"`
	FromFork    bool       `json:"from_fork"`
	PullNumber  int        `json:"pull_number,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at"`
}

type Job struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

type RunsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Page       int    `query:"page" json:"-"`
	PerPage    int    `query:"per_page" json:"-"`
	Workflow   string `query:"workflow" json:"-"` // its path
	Branch     string `query:"branch" json:"-"`
	Event      string `query:"event" json:"-"`
	Status     string `query:"status" json:"-"` // queued, waiting, running, success, failure, cancelled
	Pull       int    `query:"pull" json:"-"`   // runs of a pull request
}

type RunsResponse struct {
	Runs  []Run `json:"runs"`
	Total int64 `json:"total"`
}

type RunRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
}

type RunResponse struct {
	Run
	Jobs []Job `json:"jobs"`
}

type LogsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
	From       int    `query:"from" json:"-"`
}

type LogsResponse struct {
	Log  string `json:"log"`
	Next int    `json:"next"` // pass as ?from= to get what follows
	Done bool   `json:"done"` // the run is over: nothing will follow
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, grp, repoGrp *fiberoapi.OApiGroup, app *fiber.App) {
	tags := []string{"Actions"}
	private := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	public := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled", Description: desc + " No session needed for public repositories."}
	}
	reg := " The token registers one runner (`ogit runner register`) within an hour and is shown once."
	fiberoapi.Get(grp, "/admin/runners", c.SiteRunners, private("List shared runners", "actions.site.runners", "Site admins. Shared runners take runs from every repository."))
	fiberoapi.Post(grp, "/admin/runners/registration-token", c.SiteToken, private("Create a shared runner registration token", "actions.site.token", "Site admins."+reg))
	fiberoapi.Delete(grp, "/admin/runners/:runner_id", c.SiteDelete, private("Delete a shared runner", "actions.site.delete", "Site admins."))
	fiberoapi.Get(grp, "/group/:identifier/runners", c.GroupRunners, private("List group runners", "actions.group.runners", "The group owner. Group runners take runs from the group's repositories."))
	fiberoapi.Post(grp, "/group/:identifier/runners/registration-token", c.GroupToken, private("Create a group runner registration token", "actions.group.token", "The group owner."+reg))
	fiberoapi.Delete(grp, "/group/:identifier/runners/:runner_id", c.GroupDelete, private("Delete a group runner", "actions.group.delete", "The group owner."))
	fiberoapi.Get(repoGrp, "/:identifier/actions/runners", c.RepoRunners, private("List repository runners", "actions.repo.runners", "Maintainers and owners."))
	fiberoapi.Post(repoGrp, "/:identifier/actions/runners/registration-token", c.RepoToken, private("Create a repository runner registration token", "actions.repo.token", "Maintainers and owners."+reg))
	fiberoapi.Delete(repoGrp, "/:identifier/actions/runners/:runner_id", c.RepoDelete, private("Delete a repository runner", "actions.repo.delete", "Maintainers and owners."))

	fiberoapi.Get(repoGrp, "/:identifier/actions/runs", c.Runs, public("List workflow runs", "actions.runs", "Newest first; ?page=&per_page= (at most 100)."))
	fiberoapi.Get(repoGrp, "/:identifier/actions/runs/:number", c.Run, public("Get a workflow run", "actions.run", "With its jobs."))
	fiberoapi.Get(repoGrp, "/:identifier/actions/runs/:number/logs", c.Logs, public("Read a run's log", "actions.logs", "?from= the next value of the previous call: the log so far, then what follows."))
	setupSecrets(c, grp, repoGrp)
	setupArtifacts(c, repoGrp, app)
	fiberoapi.Get(repoGrp, "/:identifier/actions/workflows", c.Workflows, public("List workflows", "actions.workflows",
		"The default branch's workflows, with their workflow_dispatch inputs, and whether the repository uses Actions."))
	fiberoapi.Post(repoGrp, "/:identifier/actions/runs/:number/rerun", c.Rerun, private("Re-run a workflow run", "actions.rerun",
		"Writers. A finished run is queued again as a new run: same workflow, event and commit."))
	fiberoapi.Post(repoGrp, "/:identifier/actions/workflows/:workflow/dispatches", c.Dispatch, private("Run a workflow", "actions.dispatch",
		"Writers. :workflow is its file name or path; the workflow needs `on: workflow_dispatch`. ref: a branch (default: the default branch); inputs: the declared ones."))
	fiberoapi.Post(repoGrp, "/:identifier/actions/runs/:number/approve", c.Approve, private("Approve a fork run", "actions.approve",
		"Maintainers and owners: a first-time contributor's fork pull request run waits for this."))
	fiberoapi.Post(repoGrp, "/:identifier/actions/runs/:number/cancel", c.Cancel, private("Cancel a workflow run", "actions.cancel", "Writers. Queued runs stop at once, running ones when their runner next reports."))
}

func authed(ctx *fiber.Ctx) (string, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", apierror.Unauthenticated()
	}
	return auth.UserID, nil
}

func toRunner(r domain.ActionRunner) Runner {
	labels := []string{}
	if r.Labels != "" {
		labels = strings.Split(r.Labels, ",")
	}
	return Runner{Id: r.Id, Name: r.Name, Scope: r.ScopeType, Labels: labels, Version: r.Version, LastSeenAt: r.LastSeenAt,
		Online: r.LastSeenAt != nil && time.Since(*r.LastSeenAt) < time.Minute, CreatedAt: r.CreatedAt}
}

func (c Controller) list(ctx *fiber.Ctx, scope func(string) (application.RunnerScope, error)) (*RunnersResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	s, err := scope(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	runners, err := c.Actions.Runners(user, s)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &RunnersResponse{Runners: make([]Runner, 0, len(runners))}
	for _, r := range runners {
		resp.Runners = append(resp.Runners, toRunner(r))
	}
	return resp, nil
}

func (c Controller) token(ctx *fiber.Ctx, scope func(string) (application.RunnerScope, error)) (*RegistrationTokenResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	s, err := scope(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	token, expires, err := c.Actions.RegistrationToken(user, s)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &RegistrationTokenResponse{Token: token, ExpiresAt: expires}, nil
}

func (c Controller) delete(ctx *fiber.Ctx, runnerId string, scope func(string) (application.RunnerScope, error)) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	s, err := scope(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	if err := c.Actions.DeleteRunner(user, s, runnerId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "runner deleted"}, nil
}

func site(string) (application.RunnerScope, error) {
	return application.RunnerScope{Type: domain.RunnerScopeSite}, nil
}

func (c Controller) group(identifier string) func(string) (application.RunnerScope, error) {
	return func(user string) (application.RunnerScope, error) { return c.Actions.GroupScope(identifier, user) }
}

func (c Controller) repo(identifier string) func(string) (application.RunnerScope, error) {
	return func(user string) (application.RunnerScope, error) {
		id, path := repoid.Split(identifier)
		return c.Actions.RepositoryScope(id, path, user)
	}
}

func (c Controller) SiteRunners(ctx *fiber.Ctx, _ SiteRequest) (*RunnersResponse, *fiberoapi.ErrorResponse) {
	return c.list(ctx, site)
}

func (c Controller) SiteToken(ctx *fiber.Ctx, _ SiteRequest) (*RegistrationTokenResponse, *fiberoapi.ErrorResponse) {
	return c.token(ctx, site)
}

func (c Controller) SiteDelete(ctx *fiber.Ctx, req SiteRunnerRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.delete(ctx, req.RunnerId, site)
}

func (c Controller) GroupRunners(ctx *fiber.Ctx, req GroupRequest) (*RunnersResponse, *fiberoapi.ErrorResponse) {
	return c.list(ctx, c.group(req.Identifier))
}

func (c Controller) GroupToken(ctx *fiber.Ctx, req GroupRequest) (*RegistrationTokenResponse, *fiberoapi.ErrorResponse) {
	return c.token(ctx, c.group(req.Identifier))
}

func (c Controller) GroupDelete(ctx *fiber.Ctx, req GroupRunnerRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.delete(ctx, req.RunnerId, c.group(req.Identifier))
}

func (c Controller) RepoRunners(ctx *fiber.Ctx, req RepoRequest) (*RunnersResponse, *fiberoapi.ErrorResponse) {
	return c.list(ctx, c.repo(req.Identifier))
}

func (c Controller) RepoToken(ctx *fiber.Ctx, req RepoRequest) (*RegistrationTokenResponse, *fiberoapi.ErrorResponse) {
	return c.token(ctx, c.repo(req.Identifier))
}

func (c Controller) RepoDelete(ctx *fiber.Ctx, req RepoRunnerRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.delete(ctx, req.RunnerId, c.repo(req.Identifier))
}

func toRun(r *domain.ActionRun) Run {
	status := r.Status
	if r.NeedsApproval && status == domain.RunQueued {
		status = "waiting"
	}
	return Run{Id: r.Id, Number: r.Number, WorkflowPath: r.WorkflowPath, WorkflowName: r.WorkflowName, Event: r.Event, Ref: r.Ref,
		Sha: r.Sha, Actor: r.ActorName, Status: status, CancelAsked: r.CancelAsked, FromFork: r.FromFork, PullNumber: r.PullNumber,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt}
}

type DispatchRequest struct {
	Identifier string            `path:"identifier" json:"-" validate:"required"`
	Workflow   string            `path:"workflow" json:"-" validate:"required"`
	Ref        string            `json:"ref"`
	Inputs     map[string]string `json:"inputs"`
}

func (c Controller) Dispatch(ctx *fiber.Ctx, req DispatchRequest) (*Run, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	run, err := c.Actions.Dispatch(id, path, user, req.Workflow, req.Ref, req.Inputs)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	r := toRun(run)
	return &r, nil
}

type WorkflowInput struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Default     string   `json:"default"`
	Type        string   `json:"type"`
	Options     []string `json:"options"`
}

type Workflow struct {
	Path     string          `json:"path"`
	Name     string          `json:"name"`
	Dispatch bool            `json:"dispatch"` // can be run by hand
	Inputs   []WorkflowInput `json:"inputs"`
	Error    string          `json:"error,omitempty"`
}

type WorkflowsResponse struct {
	// UsesActions: the repository has workflows or runs (the UI shows the
	// Actions tab).
	UsesActions bool       `json:"uses_actions"`
	Workflows   []Workflow `json:"workflows"`
}

func (c Controller) Workflows(ctx *fiber.Ctx, req RepoRequest) (*WorkflowsResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	list, used, err := c.Actions.Workflows(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &WorkflowsResponse{UsesActions: used, Workflows: make([]Workflow, 0, len(list))}
	for _, w := range list {
		out := Workflow{Path: w.Path, Name: w.Name, Dispatch: w.Dispatch, Error: w.Error, Inputs: []WorkflowInput{}}
		for _, in := range w.Inputs {
			opts := in.Options
			if opts == nil {
				opts = []string{}
			}
			out.Inputs = append(out.Inputs, WorkflowInput{Name: in.Name, Description: in.Description, Required: in.Required, Default: in.Default, Type: in.Type, Options: opts})
		}
		resp.Workflows = append(resp.Workflows, out)
	}
	return resp, nil
}

func (c Controller) Rerun(ctx *fiber.Ctx, req RunRequest) (*Run, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	run, err := c.Actions.Rerun(id, path, user, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	r := toRun(run)
	return &r, nil
}

func (c Controller) Approve(ctx *fiber.Ctx, req RunRequest) (*Run, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	run, err := c.Actions.Approve(id, path, user, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	r := toRun(run)
	return &r, nil
}

func (c Controller) Runs(ctx *fiber.Ctx, req RunsRequest) (*RunsResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	perPage := req.PerPage
	if perPage <= 0 {
		perPage = 30
	}
	f := domain.RunFilter{WorkflowPath: req.Workflow, Event: req.Event, Status: req.Status, PullNumber: req.Pull}
	if req.Branch != "" {
		f.Ref = "refs/heads/" + req.Branch
	}
	_, runs, total, err := c.Actions.Runs(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), f, (max(req.Page, 1)-1)*perPage, perPage)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &RunsResponse{Runs: make([]Run, 0, len(runs)), Total: total}
	for i := range runs {
		resp.Runs = append(resp.Runs, toRun(&runs[i]))
	}
	return resp, nil
}

func number(s string) (int, *fiberoapi.ErrorResponse) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: "run not found", Type: "RUN_NOT_FOUND"}
	}
	return n, nil
}

func (c Controller) Run(ctx *fiber.Ctx, req RunRequest) (*RunResponse, *fiberoapi.ErrorResponse) {
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	_, run, jobs, err := c.Actions.Run(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &RunResponse{Run: toRun(run), Jobs: make([]Job, 0, len(jobs))}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, Job{Name: j.Name, Status: j.Status, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt})
	}
	return resp, nil
}

func (c Controller) Logs(ctx *fiber.Ctx, req LogsRequest) (*LogsResponse, *fiberoapi.ErrorResponse) {
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	reader := optauth.Reader(ctx, c.SessionApp, c.TokenApp)
	// Read the status first: a run that is over has all of its log.
	_, run, _, err := c.Actions.Run(id, path, reader, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	log, next, err := c.Actions.Logs(id, path, reader, n, req.From)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &LogsResponse{Log: log, Next: next, Done: domain.RunDone(run.Status)}, nil
}

func (c Controller) Cancel(ctx *fiber.Ctx, req RunRequest) (*Run, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	run, err := c.Actions.Cancel(id, path, user, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	r := toRun(run)
	return &r, nil
}
