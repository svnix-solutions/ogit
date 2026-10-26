package actrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/nektos/act/pkg/common"
	"github.com/nektos/act/pkg/model"
	"github.com/nektos/act/pkg/runner"
	"github.com/sirupsen/logrus"
)

// Options configure how runs execute.
type Options struct {
	// Platforms maps runs-on labels to Docker images.
	Platforms map[string]string
	// WorkDir holds each run's files and the action cache.
	WorkDir string
	// ContainerNetwork is the job containers' Docker network ("" for
	// Docker's default); they must reach the server's URL.
	ContainerNetwork string
	// DockerHost overrides the Docker socket.
	DockerHost string
	// ReportInterval: how often logs and job states are sent (2s if 0).
	ReportInterval time.Duration
}

// ParseLabels reads "name" or "name:docker://image" labels into names and
// platforms.
func ParseLabels(labels []string) ([]string, map[string]string) {
	names, platforms := []string{}, map[string]string{}
	for _, l := range labels {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		name, image, _ := strings.Cut(l, ":")
		names = append(names, name)
		platforms[strings.ToLower(name)] = strings.TrimPrefix(image, "docker://")
	}
	return names, platforms
}

// reporter streams a run's log and job states to the server.
type reporter struct {
	client *Client
	runId  string
	cancel context.CancelFunc
	// prefix is "<workflow name>/", which act puts before job names.
	prefix string

	mu        sync.Mutex
	buf       bytes.Buffer
	jobs      map[string]string // name -> status
	changed   map[string]bool
	cancelled bool
}

func (r *reporter) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

// Levels and Fire make reporter a logrus hook for job states.
func (r *reporter) Levels() []logrus.Level { return logrus.AllLevels }

func (r *reporter) Fire(e *logrus.Entry) error {
	name, _ := e.Data["job"].(string)
	if name == "" {
		return nil
	}
	name = strings.TrimPrefix(name, r.prefix)
	r.mu.Lock()
	defer r.mu.Unlock()
	if result, ok := e.Data["jobResult"].(string); ok {
		r.set(name, result)
	} else if _, seen := r.jobs[name]; !seen {
		r.set(name, "running")
	}
	return nil
}

func (r *reporter) set(name, status string) {
	if r.jobs[name] != status {
		r.jobs[name] = status
		r.changed[name] = true
	}
}

// flush sends what is new; status is the run's final status or "".
func (r *reporter) flush(ctx context.Context, status string) error {
	r.mu.Lock()
	data := append([]byte(nil), r.buf.Bytes()...)
	r.buf.Reset()
	var jobs []application.JobState
	for name := range r.changed {
		jobs = append(jobs, application.JobState{Name: name, Status: r.jobs[name]})
	}
	r.changed = map[string]bool{}
	r.mu.Unlock()
	if len(data) > 0 {
		if err := r.client.Log(ctx, r.runId, data); err != nil {
			return err
		}
	}
	cancel, err := r.client.State(ctx, r.runId, application.StateUpdate{Jobs: jobs, Status: status})
	if err != nil {
		return err
	}
	if cancel && status == "" {
		r.mu.Lock()
		r.cancelled = true
		r.mu.Unlock()
		r.cancel()
	}
	return nil
}

// logFormatter writes "[job] message" lines, and steps' output as is.
type logFormatter struct{ prefix string }

func (f logFormatter) Format(e *logrus.Entry) ([]byte, error) {
	msg := e.Message
	if e.Data["raw_output"] == true {
		if !strings.HasSuffix(msg, "\n") {
			msg += "\n"
		}
		return []byte(msg), nil
	}
	job, _ := e.Data["job"].(string)
	line := strings.TrimRight(msg, "\n")
	if job != "" {
		line = "[" + strings.TrimPrefix(job, f.prefix) + "] " + line
	}
	return []byte(line + "\n"), nil
}

// WithJobLogger gives act's job loggers our output and hook.
func (r *reporter) WithJobLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(r)
	l.SetLevel(logrus.InfoLevel)
	l.SetFormatter(logFormatter{prefix: r.prefix})
	l.AddHook(r)
	return l
}

// Execute runs a task with act and reports it; it returns the run's status.
func Execute(ctx context.Context, client *Client, task *application.Task, opts Options) string {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rep := &reporter{client: client, runId: task.RunId, cancel: cancel, jobs: map[string]string{}, changed: map[string]bool{}}
	interval := opts.ReportInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = rep.flush(context.Background(), "")
			}
		}
	}()

	err := run(ctx, rep, task, opts)
	close(done)
	status := domain.RunSuccess
	rep.mu.Lock()
	cancelled := rep.cancelled
	rep.mu.Unlock()
	switch {
	case cancelled:
		status = domain.RunCancelled
		fmt.Fprintln(rep, "The run was cancelled.")
	case err != nil:
		status = domain.RunFailure
		fmt.Fprintf(rep, "The run failed: %v\n", err)
	}
	// The final report must arrive, even if the run's context ended.
	for i := 0; i < 5; i++ {
		if rep.flush(context.Background(), status) == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	return status
}

func run(ctx context.Context, rep *reporter, task *application.Task, opts Options) error {
	dir, err := os.MkdirTemp(opts.WorkDir, "run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	workflowDir, workspace := filepath.Join(dir, "workflow"), filepath.Join(dir, "workspace")
	for _, d := range []string{workflowDir, workspace} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	workflowPath := filepath.Join(workflowDir, filepath.Base(task.WorkflowPath))
	if err := os.WriteFile(workflowPath, []byte(task.Workflow), 0o600); err != nil {
		return err
	}
	eventPath := filepath.Join(dir, "event.json")
	if err := os.WriteFile(eventPath, task.EventPayload, 0o600); err != nil {
		return err
	}

	planner, err := model.NewWorkflowPlanner(workflowPath, true, false)
	if err != nil {
		return fmt.Errorf("reading the workflow: %w", err)
	}
	plan, err := planner.PlanEvent(task.Event)
	if err != nil {
		return fmt.Errorf("planning the workflow: %w", err)
	}
	if plan == nil || len(plan.Stages) == 0 || len(plan.Stages[0].Runs) == 0 {
		return errors.New("the workflow has no job for this event")
	}
	rep.mu.Lock()
	rep.prefix = plan.Stages[0].Runs[0].Workflow.Name + "/"
	rep.mu.Unlock()

	owner, _, _ := strings.Cut(task.Repository, "/")
	ref := plumbingShort(task.Ref)
	secrets := map[string]string{"GITHUB_TOKEN": task.Token}
	for k, v := range task.Secrets {
		secrets[k] = v
	}
	cfg := &runner.Config{
		Actor:         task.Actor,
		Workdir:       workspace,
		EventName:     task.Event,
		EventPath:     eventPath,
		DefaultBranch: task.DefaultBranch,
		Env: map[string]string{
			"GITHUB_SERVER_URL":       task.ServerURL,
			"GITHUB_API_URL":          task.ServerURL + "/api/v3",
			"GITHUB_GRAPHQL_URL":      task.ServerURL + "/api/graphql",
			"GITHUB_REPOSITORY":       task.Repository,
			"GITHUB_REPOSITORY_OWNER": owner,
			"GITHUB_REF":              task.Ref,
			"GITHUB_REF_NAME":         ref.name,
			"GITHUB_REF_TYPE":         ref.kind,
			"SHA_REF":                 task.Sha,
			"GITHUB_RUN_NUMBER":       fmt.Sprint(task.Number),
			"GITHUB_RUN_ID":           runID(task),
		},
		Secrets:    secrets,
		Vars:       task.Vars,
		Token:      task.Token,
		Platforms:  opts.Platforms,
		AutoRemove: true,
		LogOutput:  true,
		// act passes ACTIONS_RUNTIME_URL/TOKEN (set below) to jobs only when
		// this is set; it starts no server of its own here.
		ArtifactServerPath:    dir,
		NoSkipCheckout:        true,
		ContainerDaemonSocket: opts.DockerHost,
		ContainerNetworkMode:  containerNetwork(opts.ContainerNetwork),
		ActionCache:           actionCache{inner: runner.GoGitActionCache{Path: filepath.Join(opts.WorkDir, "actions")}, server: task.ServerURL},
		GitHubInstance:        hostOf(task.ServerURL),
	}
	// act hands these to job containers from the process environment:
	// the artifact service (this server) and its token. One run at a time.
	os.Setenv("ACTIONS_RUNTIME_URL", strings.TrimSuffix(task.ServerURL, "/")+"/")
	os.Setenv("ACTIONS_RUNTIME_TOKEN", task.RuntimeToken)
	r, err := runner.New(cfg)
	if err != nil {
		return err
	}
	ctx = runner.WithJobLoggerFactory(ctx, rep)
	logger := rep.WithJobLogger()
	ctx = common.WithLogger(ctx, logger)
	return r.NewPlanExecutor(plan)(ctx)
}

// runID is GITHUB_RUN_ID: the run's numeric API id (clients parse it).
func runID(task *application.Task) string {
	if task.RunApiId > 0 {
		return fmt.Sprint(task.RunApiId)
	}
	return task.RunId
}

type refName struct{ name, kind string }

func plumbingShort(ref string) refName {
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		return refName{strings.TrimPrefix(ref, "refs/heads/"), "branch"}
	case strings.HasPrefix(ref, "refs/tags/"):
		return refName{strings.TrimPrefix(ref, "refs/tags/"), "tag"}
	}
	return refName{ref, ""}
}

func hostOf(url string) string {
	url = strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	host, _, _ := strings.Cut(url, "/")
	return host
}
