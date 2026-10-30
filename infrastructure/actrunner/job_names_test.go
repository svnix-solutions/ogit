package actrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nektos/act/pkg/model"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Two runs of the same workflow get different job names (act names job
// containers after them), which the log and the reports don't show.
func TestUniqueJobNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`name: CI
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
  test:
    name: Unit tests
    needs: build
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
`), 0o600))
	plan := func(suffix string) *model.Plan {
		planner, err := model.NewWorkflowPlanner(path, true, false)
		require.NoError(t, err)
		p, err := planner.PlanEvent("push")
		require.NoError(t, err)
		uniqueJobNames(p, suffix)
		return p
	}
	a, b := plan(" ~run-a"), plan(" ~run-b")
	names := func(p *model.Plan) []string {
		var out []string
		for _, stage := range p.Stages {
			for _, run := range stage.Runs {
				out = append(out, run.String())
			}
		}
		return out
	}
	assert.Equal(t, []string{"build ~run-a", "Unit tests ~run-a"}, names(a))
	assert.Equal(t, []string{"build ~run-b", "Unit tests ~run-b"}, names(b))
	assert.Equal(t, "test", a.Stages[1].Runs[0].JobID, "github.job doesn't change")

	rep := &reporter{prefix: "CI/", suffix: " ~run-a", jobs: map[string]string{}, changed: map[string]bool{}}
	require.NoError(t, rep.Fire(&logrus.Entry{Data: logrus.Fields{"job": "CI/Unit tests ~run-a-1", "jobResult": "success"}}))
	assert.Equal(t, map[string]string{"Unit tests-1": "success"}, rep.jobs, "statuses keep the job's name")
	line, err := logFormatter{prefix: "CI/", suffix: " ~run-a"}.Format(&logrus.Entry{Message: "Cleaning up container for job build ~run-a", Data: logrus.Fields{"job": "CI/build ~run-a"}})
	require.NoError(t, err)
	assert.Equal(t, "[build] Cleaning up container for job build\n", string(line))
}
