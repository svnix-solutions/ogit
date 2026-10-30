package server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ciWorkflow = `name: CI
on:
  push:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: echo hello
`

func actionsAPI(repo string) string { return repoAPI(repo) + "/actions" }

// registerRunner gets a registration token at tokenPath as as, and
// registers a runner with labels.
func (s *testServer) registerRunner(t *testing.T, as user, tokenPath, name string, labels ...string) *actrunner.Client {
	t.Helper()
	status, body := s.post(t, tokenPath, as.session, map[string]any{})
	require.Equal(t, http.StatusOK, status, "%v", body)
	c := &actrunner.Client{URL: s.url}
	reg, err := c.Register(context.Background(), body["token"].(string), name, labels, "test", false)
	require.NoError(t, err)
	return &actrunner.Client{URL: s.url, Token: reg.Token}
}

func fetch0(t *testing.T, c *actrunner.Client, labels ...string) *application.Task {
	t.Helper()
	task, err := c.Fetch(context.Background(), labels, "test", 0)
	require.NoError(t, err)
	return task
}

func (s *testServer) runs(t *testing.T, as user, repo string) []map[string]any {
	t.Helper()
	status, body := s.get(t, actionsAPI(repo)+"/runs", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	var out []map[string]any
	for _, r := range body["runs"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestActionsRunnerRegistration(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	tokenPath := actionsAPI("owner/app") + "/runners/registration-token"

	t.Run("who", func(t *testing.T) {
		status, _ := srv.post(t, tokenPath, f.dev.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "developers don't manage runners")
		status, _ = srv.post(t, tokenPath, f.outer.session, map[string]any{})
		assert.Equal(t, http.StatusNotFound, status, "private")
		status, _ = srv.post(t, tokenPath, f.maint.session, map[string]any{})
		assert.Equal(t, http.StatusOK, status, "maintainers do")
		status, _ = srv.post(t, "/api/v1/admin/runners/registration-token", f.owner.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "shared runners: admins only")
	})

	t.Run("once", func(t *testing.T) {
		status, body := srv.post(t, tokenPath, f.owner.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		c := &actrunner.Client{URL: srv.url}
		_, err := c.Register(context.Background(), body["token"].(string), "one", []string{"ubuntu-latest"}, "test", false)
		require.NoError(t, err)
		_, err = c.Register(context.Background(), body["token"].(string), "two", nil, "test", false)
		assert.Error(t, err, "a registration token works once")
		_, err = c.Register(context.Background(), "ogitreg_nope", "three", nil, "test", false)
		assert.Error(t, err)
	})

	t.Run("expiry", func(t *testing.T) {
		status, body := srv.post(t, tokenPath, f.owner.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		defer func() { srv.deps.ActionsApp.Now = nil }()
		_, err := (&actrunner.Client{URL: srv.url}).Register(context.Background(), body["token"].(string), "late", nil, "test", false)
		assert.Error(t, err)
	})

	t.Run("list_and_delete", func(t *testing.T) {
		c := srv.registerRunner(t, f.owner, tokenPath, "builder", "ubuntu-latest")
		status, body := srv.get(t, actionsAPI("owner/app")+"/runners", f.maint.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		var id string
		for _, r := range body["runners"].([]any) {
			if r.(map[string]any)["name"] == "builder" {
				id = r.(map[string]any)["id"].(string)
				assert.Equal(t, []any{"ubuntu-latest"}, r.(map[string]any)["labels"])
				assert.Equal(t, true, r.(map[string]any)["online"])
			}
		}
		require.NotEmpty(t, id)
		status, _ = srv.get(t, actionsAPI("owner/app")+"/runners", f.dev.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/runners/"+id, f.owner.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "not through another scope")
		status, _ = srv.request(t, http.MethodDelete, actionsAPI("owner/app")+"/runners/"+id, f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		_, err := c.Fetch(context.Background(), nil, "test", 0)
		assert.Error(t, err, "a deleted runner's token stops working")
	})
}

func TestActionsRuns(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	runner := srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "r1", "ubuntu-latest")

	pushFiles(t, f.url, f.owner.git, "main", "add CI", map[string]string{".github/workflows/ci.yml": ciWorkflow})
	runs := srv.runs(t, f.reporter, "owner/app")
	require.Len(t, runs, 1)
	assert.Equal(t, "CI", runs[0]["workflow_name"])
	assert.Equal(t, "queued", runs[0]["status"])
	assert.Equal(t, "refs/heads/main", runs[0]["ref"])
	assert.EqualValues(t, 1, runs[0]["number"])

	t.Run("filters", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "feature", "work", map[string]string{"a.txt": "a\n"})
		assert.Len(t, srv.runs(t, f.owner, "owner/app"), 1, "branches: [main] only")
		status, _ := srv.get(t, actionsAPI("owner/app")+"/runs", f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "private")
	})

	t.Run("labels", func(t *testing.T) {
		other := srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "arm", "self-hosted")
		assert.Nil(t, fetch0(t, other, "self-hosted"), "no ubuntu-latest label")
	})

	var task *application.Task
	t.Run("claim", func(t *testing.T) {
		task = fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Equal(t, "owner/app", task.Repository)
		assert.Equal(t, ciWorkflow, task.Workflow)
		assert.Equal(t, ".github/workflows/ci.yml", task.WorkflowPath)
		assert.Equal(t, "push", task.Event)
		assert.Contains(t, string(task.EventPayload), `"ref":"refs/heads/main"`)
		assert.True(t, application.IsJobToken(task.Token))
		assert.Equal(t, srv.url, task.ServerURL)
		assert.Nil(t, fetch0(t, runner, "ubuntu-latest"), "claimed once")
		assert.Equal(t, "running", srv.runs(t, f.owner, "owner/app")[0]["status"])
	})

	t.Run("job_token", func(t *testing.T) {
		auth := &githttp.BasicAuth{Username: "x-access-token", Password: task.Token}
		_, err := clone(t, f.url, auth)
		assert.NoError(t, err, "clones its repository")
		err = tryPushFiles(t, f.url, auth, "main", map[string]string{"b.txt": "b\n"})
		assert.Error(t, err, "read only")
		srv.createRepo(t, f.owner.session, "Other", true)
		_, err = clone(t, srv.url+"/owner/other.git", auth)
		assert.Error(t, err, "only its repository")

		// actions/checkout without git: GitHub's archive API, with
		// "Authorization: token <GITHUB_TOKEN>".
		for _, scheme := range []string{"token ", "Bearer "} {
			status, header, body := fetch(t, srv.url+"/api/v3/repos/owner/app/tarball/"+task.Sha, map[string]string{"Authorization": scheme + task.Token})
			assert.Equal(t, http.StatusOK, status, scheme)
			assert.Equal(t, "application/gzip", header.Get("Content-Type"))
			assert.NotEmpty(t, body)
		}
		status, _, _ := fetch(t, srv.url+"/api/v3/repos/owner/app/zipball/refs/heads/main", map[string]string{"Authorization": "token " + task.Token})
		assert.Equal(t, http.StatusOK, status)
		status, _, _ = fetch(t, srv.url+"/api/v3/repos/owner/other/tarball/main", map[string]string{"Authorization": "token " + task.Token})
		assert.Equal(t, http.StatusNotFound, status, "only its repository")
		status, _, _ = fetch(t, srv.url+"/api/v3/repos/owner/app/tarball/main", nil)
		assert.Equal(t, http.StatusNotFound, status, "private")
	})

	t.Run("report", func(t *testing.T) {
		require.NoError(t, runner.Log(context.Background(), task.RunId, []byte("hello\n")))
		cancel, err := runner.State(context.Background(), task.RunId, application.StateUpdate{Jobs: []application.JobState{{Name: "test", Status: "running"}}})
		require.NoError(t, err)
		assert.False(t, cancel)
		other := srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "r2", "ubuntu-latest")
		_, err = other.State(context.Background(), task.RunId, application.StateUpdate{Status: "success"})
		assert.Error(t, err, "only its runner reports")
		assert.Error(t, other.Log(context.Background(), task.RunId, []byte("x")))

		require.NoError(t, runner.Log(context.Background(), task.RunId, []byte("bye\n")))
		_, err = runner.State(context.Background(), task.RunId, application.StateUpdate{Jobs: []application.JobState{{Name: "test", Status: "success"}}, Status: "success"})
		require.NoError(t, err)

		status, body := srv.get(t, actionsAPI("owner/app")+"/runs/1", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "success", body["status"])
		assert.NotNil(t, body["finished_at"])
		jobs := body["jobs"].([]any)
		require.Len(t, jobs, 1)
		assert.Equal(t, "test", jobs[0].(map[string]any)["name"])
		assert.Equal(t, "success", jobs[0].(map[string]any)["status"])

		status, body = srv.get(t, actionsAPI("owner/app")+"/runs/1/logs", f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "hello\nbye\n", body["log"])
		assert.Equal(t, true, body["done"])
		_, body = srv.get(t, actionsAPI("owner/app")+"/runs/1/logs?from="+fmt.Sprint(int(body["next"].(float64))), f.reporter.session)
		assert.Equal(t, "", body["log"], "nothing after next")

		_, err = clone(t, f.url, &githttp.BasicAuth{Username: "x", Password: task.Token})
		assert.Error(t, err, "the job token ends with the run")
		status, _, _ = fetch(t, srv.url+"/api/v3/repos/owner/app/tarball/main", map[string]string{"Authorization": "token " + task.Token})
		assert.Equal(t, http.StatusUnauthorized, status, "archives too")
		assert.Error(t, runner.Log(context.Background(), task.RunId, []byte("late")), "the run is over")
	})

	t.Run("cancel", func(t *testing.T) {
		pushFiles(t, f.url, f.owner.git, "main", "again", map[string]string{"c.txt": "c\n"})
		pushFiles(t, f.url, f.owner.git, "main", "and again", map[string]string{"d.txt": "d\n"})
		status, _ := srv.post(t, actionsAPI("owner/app")+"/runs/2/cancel", f.reporter.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "writers cancel")
		status, body := srv.post(t, actionsAPI("owner/app")+"/runs/2/cancel", f.dev.session, map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "cancelled", body["status"], "queued: at once")

		task := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Equal(t, 3, task.Number, "cancelled runs aren't handed out")
		status, body = srv.post(t, actionsAPI("owner/app")+"/runs/3/cancel", f.dev.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "running", body["status"])
		cancel, err := runner.State(context.Background(), task.RunId, application.StateUpdate{})
		require.NoError(t, err)
		assert.True(t, cancel, "the runner is told")
		_, err = runner.State(context.Background(), task.RunId, application.StateUpdate{Status: "cancelled"})
		require.NoError(t, err)
	})

	t.Run("requeue_unacknowledged", func(t *testing.T) {
		pushFiles(t, f.url, f.owner.git, "main", "lost answer", map[string]string{"g.txt": "g\n"})
		lost := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, lost) // as if this answer never reached the runner
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(time.Minute) }
		assert.Zero(t, srv.deps.ActionsApp.Requeue(), "not yet")
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
		assert.Equal(t, 1, srv.deps.ActionsApp.Requeue())
		srv.deps.ActionsApp.Now = nil
		_, err := clone(t, f.url, &githttp.BasicAuth{Username: "x", Password: lost.Token})
		assert.Error(t, err, "its job token ends")
		again := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, again, "queued again")
		assert.Equal(t, lost.Number, again.Number)
		_, err = runner.State(context.Background(), again.RunId, application.StateUpdate{})
		require.NoError(t, err)
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
		assert.Zero(t, srv.deps.ActionsApp.Requeue(), "its runner reported: it keeps it")
		srv.deps.ActionsApp.Now = nil
		finish(t, runner, again, "success")
	})

	t.Run("stale", func(t *testing.T) {
		pushFiles(t, f.url, f.owner.git, "main", "once more", map[string]string{"e.txt": "e\n"})
		task := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Equal(t, 0, srv.deps.ActionsApp.FailStale(), "still reporting")
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
		defer func() { srv.deps.ActionsApp.Now = nil }()
		assert.Equal(t, 1, srv.deps.ActionsApp.FailStale())
		_, body := srv.get(t, actionsAPI("owner/app")+"/runs/"+fmt.Sprint(task.Number), f.owner.session)
		assert.Equal(t, "failure", body["status"])
	})
}

func TestActionsWorkflowFiles(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", false)
	url := srv.url + "/owner/app.git"

	pushFiles(t, url, owner.git, "main", "broken", map[string]string{".github/workflows/bad.yml": "on: [push\njobs: {"})
	assert.Empty(t, srv.runs(t, owner, "owner/app"), "a broken workflow runs nothing (and the push works)")

	pushFiles(t, url, owner.git, "main", "both dirs", map[string]string{
		".github/workflows/gh.yml":  "name: GH\non: push\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n",
		".ogit/workflows/ogit.yaml": "name: oGit\non: [push]\njobs:\n  a:\n    runs-on: [self-hosted, linux]\n    steps: [{run: 'true'}]\n",
	})
	runs := srv.runs(t, owner, "owner/app")
	require.Len(t, runs, 1, ".ogit/workflows wins")
	assert.Equal(t, "oGit", runs[0]["workflow_name"])

	pushFiles(t, url, owner.git, "main", "tags only", map[string]string{".ogit/workflows/ogit.yaml": "name: Release\non:\n  push:\n    tags: ['v*']\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"})
	assert.Len(t, srv.runs(t, owner, "owner/app"), 1, "a tags filter: branch pushes don't run it")
}

func TestActionsRunnerScopes(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	srv.enableTOTP(t, alice)
	srv.promote(t, "alice")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "team"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = srv.post(t, "/api/v1/group/team/members", alice.session, map[string]any{"username": "bob", "role": "maintainer"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.createIn(t, alice, "team", "Service", true)
	require.Equal(t, http.StatusOK, status)
	srv.createRepo(t, bob.session, "Solo", true)

	status, _ = srv.post(t, "/api/v1/group/team/runners/registration-token", bob.session, map[string]any{})
	assert.Equal(t, http.StatusForbidden, status, "group runners: the owner only")
	group := srv.registerRunner(t, alice, "/api/v1/group/team/runners/registration-token", "team-runner", "ubuntu-latest")
	site := srv.registerRunner(t, alice, "/api/v1/admin/runners/registration-token", "shared", "ubuntu-latest")

	pushFiles(t, srv.url+"/bob/solo.git", bob.git, "main", "ci", map[string]string{".github/workflows/ci.yml": ciWorkflow})
	assert.Nil(t, fetch0(t, group, "ubuntu-latest"), "not the group's repository")
	solo := fetch0(t, site, "ubuntu-latest")
	require.NotNil(t, solo, "shared runners take any")
	assert.Equal(t, "bob/solo", solo.Repository)

	pushFiles(t, srv.url+"/team/service.git", alice.git, "main", "ci", map[string]string{".github/workflows/ci.yml": ciWorkflow})
	task := fetch0(t, group, "ubuntu-latest")
	require.NotNil(t, task)
	assert.Equal(t, "team/service", task.Repository)

	// A runner is deleted through its own scope only.
	srv.registerRunner(t, bob, actionsAPI("bob/solo")+"/runners/registration-token", "bobs", "ubuntu-latest")
	_, body = srv.get(t, actionsAPI("bob/solo")+"/runners", bob.session)
	id := body["runners"].([]any)[0].(map[string]any)["id"].(string)
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/runners/"+id, alice.session, nil)
	assert.Equal(t, http.StatusNotFound, status, "not a shared runner")
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/group/team/runners/"+id, alice.session, nil)
	assert.Equal(t, http.StatusNotFound, status, "not the group's")
	_, body = srv.get(t, actionsAPI("bob/solo")+"/runners", bob.session)
	assert.Len(t, body["runners"], 1)
}

func TestActionsConcurrentClaims(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", true)
	var runners []*actrunner.Client
	for i := 0; i < 4; i++ {
		runners = append(runners, srv.registerRunner(t, owner, actionsAPI("owner/app")+"/runners/registration-token", "r", "ubuntu-latest"))
	}
	pushFiles(t, srv.url+"/owner/app.git", owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": ciWorkflow})

	var mu sync.Mutex
	got := 0
	var wg sync.WaitGroup
	for _, r := range runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := r.Fetch(context.Background(), []string{"ubuntu-latest"}, "test", 0)
			assert.NoError(t, err)
			if task != nil {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, got, "one run, one runner")
	var run domain.ActionRun
	require.NoError(t, srv.deps.Database.Db.First(&run).Error)
	assert.Equal(t, domain.RunRunning, run.Status)
	ok, err := srv.deps.ActionsApp.Pers.ClaimRun(run.Id, *run.RunnerId, "hash", time.Now())
	require.NoError(t, err)
	assert.False(t, ok, "a running run can't be claimed again")
}
