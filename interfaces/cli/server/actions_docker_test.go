//go:build integration

package server

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActionsRunnerDocker runs workflows with act in Docker, as `ogit
// runner run` does: logs, job states and the result reach the server.
func TestActionsRunnerDocker(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.newUser(t, "owner")
	srv.createRepo(t, owner.session, "App", true)
	url := srv.url + "/owner/app.git"
	runner := srv.registerRunner(t, owner, actionsAPI("owner/app")+"/runners/registration-token", "docker", "ubuntu-latest")
	opts := actrunner.Options{Platforms: map[string]string{"ubuntu-latest": "alpine:3.20"}, WorkDir: t.TempDir(), ReportInterval: 200 * time.Millisecond}

	run := func(t *testing.T, number int) (string, map[string]any) {
		t.Helper()
		task, err := runner.Fetch(context.Background(), []string{"ubuntu-latest"}, "test", 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, task)
		require.Equal(t, number, task.Number)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		status := actrunner.Execute(ctx, runner, task, opts)
		_, body := srv.get(t, actionsAPI("owner/app")+"/runs/"+strconv.Itoa(number)+"/logs", owner.session)
		return status, body
	}

	t.Run("success", func(t *testing.T) {
		pushFiles(t, url, owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": `name: CI
on: push
defaults:
  run:
    shell: sh
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "hello from $GITHUB_REPOSITORY on $GITHUB_REF_NAME"
      - run: echo "secret is $TOKEN"
        env:
          TOKEN: ${{ secrets.GITHUB_TOKEN }}
  after:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - run: echo second job
`})
		status, logs := run(t, 1)
		assert.Equal(t, "success", status, "%v", logs["log"])
		log := logs["log"].(string)
		assert.Contains(t, log, "hello from owner/app on main")
		assert.Contains(t, log, "second job")
		assert.NotContains(t, log, "ogitj_", "the job token is masked")
		assert.Contains(t, log, "secret is ***")
		assert.Contains(t, log, "[build]")

		status2, body := srv.get(t, actionsAPI("owner/app")+"/runs/1", owner.session)
		require.Equal(t, http.StatusOK, status2)
		assert.Equal(t, "success", body["status"])
		names := map[string]any{}
		for _, j := range body["jobs"].([]any) {
			names[j.(map[string]any)["name"].(string)] = j.(map[string]any)["status"]
		}
		assert.Equal(t, map[string]any{"build": "success", "after": "success"}, names)
	})

	t.Run("failure", func(t *testing.T) {
		pushFiles(t, url, owner.git, "main", "break", map[string]string{".github/workflows/ci.yml": `on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo failing; exit 3
        shell: sh
`})
		status, logs := run(t, 2)
		assert.Equal(t, "failure", status, "%v", logs["log"])
		_, body := srv.get(t, actionsAPI("owner/app")+"/runs/2", owner.session)
		assert.Equal(t, "failure", body["status"])
	})

	t.Run("pull_request", func(t *testing.T) {
		pushFiles(t, url, owner.git, "main", "pr ci", map[string]string{".github/workflows/ci.yml": `on: pull_request
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "pr $GITHUB_REF from $GITHUB_HEAD_REF into $GITHUB_BASE_REF secret=$SECRET"
        shell: sh
        env:
          SECRET: ${{ secrets.DEPLOY }}
`})
		status, _ := srv.request(t, http.MethodPut, actionsAPI("owner/app")+"/secrets/DEPLOY", owner.session, map[string]any{"value": "hunter2"})
		require.Equal(t, http.StatusOK, status)
		pushFiles(t, url, owner.git, "topic", "work", map[string]string{"a.txt": "a\n"})
		status, body := srv.post(t, pullsAPI("owner/app"), owner.session, map[string]any{"title": "Topic", "source": "topic"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		result, logs := run(t, 3)
		assert.Equal(t, "success", result, "%v", logs["log"])
		log := logs["log"].(string)
		assert.Contains(t, log, "pr refs/pull/1/merge from topic into main secret=***")
		assert.NotContains(t, log, "hunter2")
	})

	t.Run("artifacts", func(t *testing.T) {
		node := opts
		node.Platforms = map[string]string{"ubuntu-latest": "node:20-alpine"}
		pushFiles(t, url, owner.git, "main", "artifacts", map[string]string{".github/workflows/ci.yml": `on: push
defaults:
  run:
    shell: sh
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: mkdir -p out/sub && echo "hello artifact" > out/a.txt && head -c 300000 /dev/urandom > out/sub/blob.bin && sha256sum out/sub/blob.bin | cut -d' ' -f1 > sum.txt
      - uses: ogit/upload-artifact@v4
        with:
          name: out
          path: out
      - uses: ogit/upload-artifact@v4
        with:
          name: sum
          path: sum.txt
  use:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - uses: ogit/download-artifact@v4
        with:
          path: got
      - run: cat got/out/a.txt && test "$(sha256sum got/out/sub/blob.bin | cut -d' ' -f1)" = "$(cat got/sum/sum.txt)" && echo "checksums match"
`})
		task, err := runner.Fetch(context.Background(), []string{"ubuntu-latest"}, "test", 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, task)
		if runtime.GOOS == "darwin" {
			// Docker Desktop: job containers reach the host by this name.
			task.ServerURL = strings.Replace(task.ServerURL, "127.0.0.1", "host.docker.internal", 1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		status := actrunner.Execute(ctx, runner, task, node)
		_, logs := srv.get(t, actionsAPI("owner/app")+"/runs/"+strconv.Itoa(task.Number)+"/logs", owner.session)
		require.Equal(t, "success", status, "%v", logs["log"])
		assert.Contains(t, logs["log"], "hello artifact")
		assert.Contains(t, logs["log"], "checksums match")
		_, body := srv.get(t, actionsAPI("owner/app")+"/runs/"+strconv.Itoa(task.Number)+"/artifacts", owner.session)
		names := []string{}
		for _, a := range body["artifacts"].([]any) {
			names = append(names, a.(map[string]any)["name"].(string))
		}
		assert.Equal(t, []string{"out", "sum"}, names)
	})
}
