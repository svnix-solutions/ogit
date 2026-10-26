package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func workflow(on string) string {
	return "name: CI\non:\n" + on + "\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: 'true'\n"
}

func (f *pullFixture) runner(t *testing.T) *actrunner.Client {
	t.Helper()
	return f.srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "r", "ubuntu-latest")
}

// finish reports a run's job and result.
func finish(t *testing.T, r *actrunner.Client, task *application.Task, status string) {
	t.Helper()
	_, err := r.State(context.Background(), task.RunId, application.StateUpdate{Jobs: []application.JobState{{Name: "test", Status: status}}, Status: status})
	require.NoError(t, err)
}

func TestActionsSecrets(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	secrets := actionsAPI("owner/app") + "/secrets/"

	t.Run("who", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPut, secrets+"TOKEN", f.dev.session, map[string]any{"value": "x"})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodPut, secrets+"TOKEN", f.outer.session, map[string]any{"value": "x"})
		assert.Equal(t, http.StatusNotFound, status)
		status, body := srv.request(t, http.MethodPut, secrets+"deploy_key", f.maint.session, map[string]any{"value": "repo-secret"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "DEPLOY_KEY", body["name"])
		status, body = srv.request(t, http.MethodPut, secrets+"GITHUB_TOKEN", f.maint.session, map[string]any{"value": "x"})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		status, _ = srv.request(t, http.MethodPut, secrets+"1BAD", f.maint.session, map[string]any{"value": "x"})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("write_only", func(t *testing.T) {
		status, body := srv.get(t, actionsAPI("owner/app")+"/secrets", f.maint.session)
		require.Equal(t, http.StatusOK, status)
		raw, _ := json.Marshal(body)
		assert.Contains(t, string(raw), "DEPLOY_KEY")
		assert.NotContains(t, string(raw), "repo-secret", "values are never read back")
		var stored domain.ActionSecret
		require.NoError(t, srv.deps.Database.Db.Where("name = ?", "DEPLOY_KEY").First(&stored).Error)
		assert.NotContains(t, stored.Value, "repo-secret", "sealed at rest")
	})

	t.Run("runs_get_them", func(t *testing.T) {
		srv.promote(t, "owner")
		status, _ := srv.request(t, http.MethodPut, "/api/v1/admin/actions/secrets/DEPLOY_KEY", f.owner.session, map[string]any{"value": "site-secret"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPut, "/api/v1/admin/actions/secrets/SITE_ONLY", f.owner.session, map[string]any{"value": "s"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodPut, "/api/v1/admin/actions/secrets/X", f.maint.session, map[string]any{"value": "s"})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodPut, actionsAPI("owner/app")+"/variables/region", f.maint.session, map[string]any{"value": "eu"})
		require.Equal(t, http.StatusOK, status)
		_, body := srv.get(t, actionsAPI("owner/app")+"/variables", f.maint.session)
		assert.Equal(t, "eu", body["variables"].([]any)[0].(map[string]any)["value"], "variables are readable")

		r := f.runner(t)
		pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": workflow("  push:")})
		task := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Equal(t, map[string]string{"DEPLOY_KEY": "repo-secret", "SITE_ONLY": "s"}, task.Secrets, "the repository's wins")
		assert.Equal(t, map[string]string{"REGION": "eu"}, task.Vars)
	})

	t.Run("delete", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, secrets+"DEPLOY_KEY", f.maint.session, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.request(t, http.MethodDelete, secrets+"DEPLOY_KEY", f.maint.session, nil)
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestActionsSecretsNeedEncryptionKey(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) { c.Security.EncryptionKey = "" })
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", true)
	status, body := srv.request(t, http.MethodPut, actionsAPI("owner/app")+"/secrets/X", owner.session, map[string]any{"value": "x"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "SECRETS_UNAVAILABLE", body["type"])
}

func TestActionsTokenPermissionsAndStatuses(t *testing.T) {
	f := newPullFixture(t, true, withPublicURL)
	srv := f.srv
	r := f.runner(t)
	writer := "name: CI\non: push\npermissions:\n  contents: write\n  statuses: write\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": writer})
	task := fetch0(t, r, "ubuntu-latest")
	require.NotNil(t, task)
	auth := &githttp.BasicAuth{Username: "x-access-token", Password: task.Token}

	t.Run("writes", func(t *testing.T) {
		require.NoError(t, tryPushFiles(t, f.url, auth, "main", map[string]string{"generated.txt": "x\n"}), "contents: write")
		assert.Len(t, srv.runs(t, f.owner, "owner/app"), 1, "its push starts no workflow")
		status, body, _ := srv.do(t, http.MethodPost, "/api/v3/repos/owner/app/statuses/"+task.Sha, map[string]string{"Authorization": "token " + task.Token},
			map[string]any{"state": "success", "context": "lint"})
		assert.Equal(t, http.StatusCreated, status, "statuses: write: %v", body)
	})

	t.Run("job_statuses", func(t *testing.T) {
		_, err := r.State(context.Background(), task.RunId, application.StateUpdate{Jobs: []application.JobState{{Name: "test", Status: "running"}}})
		require.NoError(t, err)
		status, body := srv.get(t, repoAPI("owner/app")+"/status?ref="+task.Sha, f.owner.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		contexts := map[string]any{}
		for _, s := range body["statuses"].([]any) {
			contexts[s.(map[string]any)["context"].(string)] = s.(map[string]any)["state"]
		}
		assert.Equal(t, "pending", contexts["CI / test (push)"])
		finish(t, r, task, "success")
		_, body = srv.get(t, repoAPI("owner/app")+"/status?ref="+task.Sha, f.owner.session)
		for _, s := range body["statuses"].([]any) {
			if s.(map[string]any)["context"] == "CI / test (push)" {
				assert.Equal(t, "success", s.(map[string]any)["state"])
				assert.Equal(t, "https://git.example.com/owner/app/actions/runs/1", s.(map[string]any)["target_url"])
			}
		}
	})

	t.Run("read_only_by_default", func(t *testing.T) {
		pushFiles(t, f.url, f.owner.git, "main", "plain", map[string]string{".github/workflows/ci.yml": workflow("  push:")})
		task := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		auth := &githttp.BasicAuth{Username: "x-access-token", Password: task.Token}
		assert.Error(t, tryPushFiles(t, f.url, auth, "main", map[string]string{"x.txt": "x\n"}))
		status, _, _ := srv.do(t, http.MethodPost, "/api/v3/repos/owner/app/statuses/"+task.Sha, map[string]string{"Authorization": "token " + task.Token},
			map[string]any{"state": "success", "context": "lint"})
		assert.Equal(t, http.StatusForbidden, status)

		// A run that ends with a job still running fails it.
		_, err := r.State(context.Background(), task.RunId, application.StateUpdate{Jobs: []application.JobState{{Name: "test", Status: "running"}}})
		require.NoError(t, err)
		_, err = r.State(context.Background(), task.RunId, application.StateUpdate{Status: "cancelled"})
		require.NoError(t, err)
		_, body := srv.get(t, repoAPI("owner/app")+"/status?ref="+task.Sha, f.owner.session)
		assert.Equal(t, "failure", body["state"], "cancelled jobs are errors")
	})
}

func TestActionsPullRequests(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	r := f.runner(t)
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{
		".github/workflows/pr.yml": workflow("  pull_request:\n    branches: [main]\n    paths: ['src/**']"),
		"src/main.go":              "package main\n",
	})
	require.Nil(t, fetch0(t, r, "ubuntu-latest"), "no push trigger")

	var task *application.Task
	t.Run("merge_ref", func(t *testing.T) {
		status, pr := f.open(t, f.owner, "feature", map[string]string{"src/a.go": "package main\n"}, nil)
		require.Equal(t, http.StatusOK, status, "%v", pr)
		task = fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Equal(t, "pull_request", task.Event)
		assert.Equal(t, "refs/pull/1/merge", task.Ref)
		st, err := srv.deps.Storage.GetStorer(srv.repoStorageKey(t, "owner/app"))
		require.NoError(t, err)
		merge, err := object.GetCommit(st, plumbing.NewHash(task.Sha))
		require.NoError(t, err)
		require.Len(t, merge.ParentHashes, 2, "the head merged into the base")
		head, _ := st.Reference(plumbing.NewBranchReferenceName("feature"))
		assert.Equal(t, head.Hash(), merge.ParentHashes[1])
		ref, err := st.Reference("refs/pull/1/merge")
		require.NoError(t, err)
		assert.Equal(t, task.Sha, ref.Hash().String())
		var payload map[string]any
		require.NoError(t, json.Unmarshal(task.EventPayload, &payload))
		assert.Equal(t, "opened", payload["action"])

		finish(t, r, task, "success")
		_, body := srv.get(t, repoAPI("owner/app")+"/status?ref="+head.Hash().String(), f.owner.session)
		assert.Equal(t, "success", body["state"], "statuses go on the head")
	})

	t.Run("filters_and_synchronize", func(t *testing.T) {
		status, _ := f.open(t, f.owner, "docs", map[string]string{"README.md": "docs\n"}, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Nil(t, fetch0(t, r, "ubuntu-latest"), "paths: src/** only")
		pushOnto(t, f.url, f.dev.git, "feature", "more", map[string]string{"src/b.go": "package main\n"})
		next := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, next, "synchronize")
		assert.Contains(t, string(next.EventPayload), `"action":"synchronize"`)
	})

	t.Run("conflict_uses_head", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "clash", "theirs", map[string]string{"src/main.go": "package clash\n"})
		pushFiles(t, f.url, f.owner.git, "main", "ours", map[string]string{"src/main.go": "package ours\n"})
		status, _ := f.open(t, f.dev, "clash", nil, nil)
		require.Equal(t, http.StatusOK, status)
		task := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Contains(t, task.Ref, "/head")
	})

	t.Run("forks", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPut, actionsAPI("owner/app")+"/secrets/KEY", f.owner.session, map[string]any{"value": "v"})
		require.Equal(t, http.StatusOK, status)
		srv.createRepo(t, f.outer.session, "Scratch", false)
		// outer can't read owner/app (private): make it public for a fork.
		status, _ = srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"is_private": false})
		require.Equal(t, http.StatusOK, status)
		srv.fork(t, f.outer, "owner/app", map[string]any{})
		pushFiles(t, srv.url+"/outer/app.git", f.outer.git, "patch", "fork work", map[string]string{"src/fork.go": "package main\n"})
		status, pr := srv.post(t, pullsAPI("owner/app"), f.outer.session, map[string]any{"title": "From a fork", "source": "patch", "source_repository": "outer/app"})
		require.Equal(t, http.StatusOK, status, "%v", pr)

		runs := srv.runs(t, f.owner, "owner/app")
		assert.Equal(t, "waiting", runs[0]["status"], "a first-time contributor")
		assert.Equal(t, true, runs[0]["from_fork"])
		assert.Nil(t, fetch0(t, r, "ubuntu-latest"), "not before approval")
		number := int(runs[0]["number"].(float64))
		status, _ = srv.post(t, actionsAPI("owner/app")+"/runs/"+itoa(number)+"/approve", f.dev.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "maintainers approve")
		status, body := srv.post(t, actionsAPI("owner/app")+"/runs/"+itoa(number)+"/approve", f.maint.session, map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "queued", body["status"])

		task := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		assert.Empty(t, task.Secrets, "no secrets for forks")
		auth := &githttp.BasicAuth{Username: "x-access-token", Password: task.Token}
		_, err := clone(t, f.url, auth)
		assert.NoError(t, err)
		finish(t, r, task, "success")

		// Once they have a merged pull request, they are trusted.
		require.NoError(t, srv.deps.Database.Db.Model(&domain.PullRequest{}).Where("number = ?", pr["number"]).Update("state", domain.PullRequestMerged).Error)
		pushFiles(t, srv.url+"/outer/app.git", f.outer.git, "patch2", "more", map[string]string{"src/fork2.go": "package main\n"})
		status, _ = srv.post(t, pullsAPI("owner/app"), f.outer.session, map[string]any{"title": "Again", "source": "patch2", "source_repository": "outer/app"})
		require.Equal(t, http.StatusOK, status)
		task = fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task, "no approval needed")
		assert.Empty(t, task.Secrets)
		auth = &githttp.BasicAuth{Username: "x-access-token", Password: task.Token}
		assert.Error(t, tryPushFiles(t, f.url, auth, "main", map[string]string{"x": "x"}), "read only")
	})
}

func itoa(n int) string { return fmt.Sprint(n) }

func TestActionsOtherEvents(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	r := f.runner(t)
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{
		".github/workflows/issues.yml":   workflow("  issues:\n    types: [opened]"),
		".github/workflows/comments.yml": workflow("  issue_comment:"),
		".github/workflows/release.yml":  workflow("  release:\n    types: [published]"),
		".github/workflows/refs.yml":     workflow("  create:\n  delete:"),
	})
	events := func() map[string]int {
		out := map[string]int{}
		for _, run := range srv.runs(t, f.owner, "owner/app") {
			out[run["event"].(string)]++
		}
		return out
	}

	issue := srv.openIssue(t, f.dev, "owner/app", "Bug")
	status, _ := srv.post(t, issuesAPI("owner/app", issue["number"], "comments"), f.dev.session, map[string]any{"body": "+1"})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.request(t, http.MethodPatch, issuesAPI("owner/app", issue["number"]), f.dev.session, map[string]any{"state": "closed"})
	require.Equal(t, http.StatusOK, status)
	pushFiles(t, f.url, f.dev.git, "topic", "branch", map[string]string{"t.txt": "t\n"})
	status, _ = srv.post(t, releasesAPI("owner/app"), f.owner.session, map[string]any{"tag_name": "v1", "name": "v1"})
	require.Equal(t, http.StatusOK, status)

	got := events()
	assert.Equal(t, 1, got["issues"], "types: [opened] only")
	assert.Equal(t, 1, got["issue_comment"])
	assert.Equal(t, 1, got["release"])
	assert.Equal(t, 2, got["create"], "the branch and the release's tag")

	task := fetch0(t, r, "ubuntu-latest")
	require.NotNil(t, task)
	assert.Equal(t, "refs/heads/main", task.Ref, "the default branch's workflows")
}

func TestActionsDispatch(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	wf := "name: Deploy\non:\n  workflow_dispatch:\n    inputs:\n      env:\n        required: true\n      dry:\n        default: 'yes'\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	pushFiles(t, f.url, f.owner.git, "main", "deploy", map[string]string{".github/workflows/deploy.yml": wf, ".github/workflows/ci.yml": workflow("  push:")})
	dispatch := actionsAPI("owner/app") + "/workflows/deploy.yml/dispatches"

	status, _ := srv.post(t, dispatch, f.reporter.session, map[string]any{"inputs": map[string]any{"env": "prod"}})
	assert.Equal(t, http.StatusForbidden, status, "writers")
	status, body := srv.post(t, dispatch, f.dev.session, map[string]any{})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "env is required: %v", body)
	status, _ = srv.post(t, dispatch, f.dev.session, map[string]any{"inputs": map[string]any{"env": "prod", "other": "x"}})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "undeclared input")
	status, _ = srv.post(t, actionsAPI("owner/app")+"/workflows/ci.yml/dispatches", f.dev.session, map[string]any{})
	assert.Equal(t, http.StatusNotFound, status, "no workflow_dispatch")
	status, _ = srv.post(t, dispatch, f.dev.session, map[string]any{"ref": "nope", "inputs": map[string]any{"env": "prod"}})
	assert.Equal(t, http.StatusNotFound, status)

	status, body = srv.post(t, dispatch, f.dev.session, map[string]any{"inputs": map[string]any{"env": "prod"}})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, "workflow_dispatch", body["event"])
	assert.Equal(t, "dev", body["actor"])
	var run domain.ActionRun
	require.NoError(t, srv.deps.Database.Db.Where("event = ?", "workflow_dispatch").First(&run).Error)
	assert.Contains(t, run.EventPayload, `"inputs":{"dry":"yes","env":"prod"}`)
}

func TestActionsSchedule(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	wf := "name: Nightly\non:\n  schedule:\n    - cron: '*/10 * * * *'\n    - cron: '* * * * *'\n    - cron: 'not a cron'\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	pushFiles(t, f.url, f.owner.git, "feature", "elsewhere", map[string]string{".github/workflows/nightly.yml": wf})
	var n int64
	srv.deps.Database.Db.Model(&domain.ActionSchedule{}).Count(&n)
	assert.Zero(t, n, "only the default branch's schedules")

	pushFiles(t, f.url, f.owner.git, "main", "nightly", map[string]string{".github/workflows/nightly.yml": wf})
	var schedules []domain.ActionSchedule
	require.NoError(t, srv.deps.Database.Db.Find(&schedules).Error)
	require.Len(t, schedules, 1, "every minute is too often; a bad cron is ignored")
	assert.Equal(t, "*/10 * * * *", schedules[0].Cron)

	assert.Equal(t, 0, srv.deps.ActionsApp.RunSchedules(), "not due yet")
	srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	defer func() { srv.deps.ActionsApp.Now = nil }()
	assert.Equal(t, 1, srv.deps.ActionsApp.RunSchedules())
	assert.Equal(t, 0, srv.deps.ActionsApp.RunSchedules(), "once per due time")
	ok, err := srv.deps.ActionsApp.Pers.ClaimSchedule(schedules[0].Id, schedules[0].NextRunAt, time.Now().Add(time.Hour).UTC())
	require.NoError(t, err)
	assert.False(t, ok, "another server already moved it")
	runs := srv.runs(t, f.owner, "owner/app")
	require.Len(t, runs, 1)
	assert.Equal(t, "schedule", runs[0]["event"])

	pushFiles(t, f.url, f.owner.git, "main", "drop", map[string]string{".github/workflows/nightly.yml": workflow("  push:\n    branches: [nope]")})
	srv.deps.Database.Db.Model(&domain.ActionSchedule{}).Count(&n)
	assert.Zero(t, n, "removed with the cron")
}
