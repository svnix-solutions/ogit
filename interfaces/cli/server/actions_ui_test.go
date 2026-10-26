package server

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActionsWorkflowsAndRuns(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	r := f.runner(t)

	_, body := srv.get(t, actionsAPI("owner/app")+"/workflows", f.reporter.session)
	assert.Equal(t, false, body["uses_actions"], "no workflows, no runs")

	deploy := "name: Deploy\non:\n  push:\n  workflow_dispatch:\n    inputs:\n      env:\n        description: Where\n        required: true\n        type: choice\n        options: [staging, prod]\n      dry:\n        type: boolean\n        default: 'true'\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{
		".github/workflows/deploy.yml": deploy,
		".github/workflows/bad.yml":    "on: [",
	})

	t.Run("workflows", func(t *testing.T) {
		status, body := srv.get(t, actionsAPI("owner/app")+"/workflows", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["uses_actions"])
		byPath := map[string]map[string]any{}
		for _, w := range body["workflows"].([]any) {
			byPath[w.(map[string]any)["path"].(string)] = w.(map[string]any)
		}
		assert.NotEmpty(t, byPath[".github/workflows/bad.yml"]["error"])
		d := byPath[".github/workflows/deploy.yml"]
		assert.Equal(t, "Deploy", d["name"])
		assert.Equal(t, true, d["dispatch"])
		assert.Equal(t, []any{
			map[string]any{"name": "env", "description": "Where", "required": true, "default": "", "type": "choice", "options": []any{"staging", "prod"}},
			map[string]any{"name": "dry", "description": "", "required": false, "default": "true", "type": "boolean", "options": []any{}},
		}, d["inputs"])
		status, _ = srv.get(t, actionsAPI("owner/app")+"/workflows", f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("filters", func(t *testing.T) {
		status, _ := srv.post(t, actionsAPI("owner/app")+"/workflows/deploy.yml/dispatches", f.dev.session, map[string]any{"inputs": map[string]any{"env": "prod"}})
		require.Equal(t, http.StatusOK, status)
		count := func(query string) int {
			status, body := srv.get(t, actionsAPI("owner/app")+"/runs?"+query, f.reporter.session)
			require.Equal(t, http.StatusOK, status, "%v", body)
			return int(body["total"].(float64))
		}
		assert.Equal(t, 2, count(""))
		assert.Equal(t, 1, count("event=workflow_dispatch"))
		assert.Equal(t, 2, count("workflow=.github/workflows/deploy.yml"))
		assert.Equal(t, 0, count("workflow=.github/workflows/other.yml"))
		assert.Equal(t, 2, count("branch=main"))
		assert.Equal(t, 2, count("status=queued"))
		assert.Equal(t, 0, count("status=waiting"))
		assert.Equal(t, 0, count("pull=1"))
	})

	t.Run("rerun_and_badge", func(t *testing.T) {
		badge := func() string {
			status, header, body := fetch(t, srv.url+"/owner/app/actions/workflows/deploy.yml/badge.svg", basic(f.reporter.git))
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, "image/svg+xml; charset=utf-8", header.Get("Content-Type"))
			return string(body)
		}
		assert.Contains(t, badge(), "no status")
		status, _, _ := fetch(t, srv.url+"/owner/app/actions/workflows/deploy.yml/badge.svg", nil)
		assert.Equal(t, http.StatusNotFound, status, "private")

		task := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, task)
		status, body := srv.post(t, actionsAPI("owner/app")+"/runs/1/rerun", f.dev.session, map[string]any{})
		assert.Equal(t, http.StatusConflict, status, "not finished: %v", body)
		finish(t, r, task, "failure")
		assert.Contains(t, badge(), "failing")
		assert.Contains(t, badge(), ">Deploy<")

		status, _ = srv.post(t, actionsAPI("owner/app")+"/runs/1/rerun", f.reporter.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "writers")
		status, body = srv.post(t, actionsAPI("owner/app")+"/runs/1/rerun", f.dev.session, map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.EqualValues(t, 3, body["number"])
		assert.Equal(t, "queued", body["status"])
		assert.Equal(t, "dev", body["actor"])
		assert.Equal(t, task.Sha, body["sha"], "the same commit")

		again := fetch0(t, r, "ubuntu-latest")
		for again != nil && again.Number != 3 {
			finish(t, r, again, "success")
			again = fetch0(t, r, "ubuntu-latest")
		}
		require.NotNil(t, again)
		finish(t, r, again, "success")
		assert.Contains(t, badge(), "passing")
		assert.Contains(t, badge(), `aria-label="Deploy: passing"`)
	})
}
