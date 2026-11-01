package server

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/persistence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withWorkspaces(cfg *config.Config) { cfg.Workspaces.Domain = "workspaces.test" }

func createWorkspace(t *testing.T, f *pullFixture, as user, branch string) string {
	t.Helper()
	status, body := f.srv.post(t, repoAPI("owner/app")+"/workspaces", as.session, map[string]any{"branch": branch})
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body["id"].(string)
}

func workspacePath(id string, rest ...string) string {
	path := "/api/v1/user/workspaces/" + id
	for _, r := range rest {
		path += "/" + r
	}
	return path
}

func TestWorkspaces(t *testing.T) {
	f := newPullFixture(t, true, withWorkspaces)
	srv := f.srv
	api := repoAPI("owner/app") + "/workspaces"

	t.Run("who can create", func(t *testing.T) {
		status, body := srv.post(t, api, f.reporter.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "readers fork first: %v", body)
		status, body = srv.post(t, api, f.outer.session, map[string]any{})
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "REPOSITORY_NOT_FOUND", body["type"])
		status, _ = srv.get(t, api, f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.get(t, "/api/v1/user/workspaces", "")
		assert.NotEqual(t, http.StatusOK, status)
	})

	t.Run("branches", func(t *testing.T) {
		status, body := srv.post(t, api, f.dev.session, map[string]any{"branch": "nope"})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "WORKSPACE_BRANCH_NOT_FOUND", body["type"])

		status, body = srv.post(t, api, f.dev.session, map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "main", body["branch"], "the default branch")
		assert.Equal(t, "stopped", body["state"])
		assert.Equal(t, "owner/app", body["repository"])

		status, body = srv.post(t, api, f.dev.session, map[string]any{"branch": "main"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "WORKSPACE_EXISTS", body["type"])
		status, _ = srv.post(t, api, f.maint.session, map[string]any{"branch": "main"})
		assert.Equal(t, http.StatusOK, status, "one per user and branch")
	})

	t.Run("private to their user", func(t *testing.T) {
		_, list := srv.get(t, api, f.dev.session)
		workspaces := list["workspaces"].([]any)
		require.Len(t, workspaces, 1)
		id := workspaces[0].(map[string]any)["id"].(string)
		for _, other := range []user{f.owner, f.maint} {
			status, body := srv.get(t, workspacePath(id), other.session)
			assert.Equal(t, http.StatusNotFound, status)
			assert.Equal(t, "WORKSPACE_NOT_FOUND", body["type"])
			for _, action := range []string{"start", "stop"} {
				status, _ = srv.post(t, workspacePath(id, action), other.session, nil)
				assert.Equal(t, http.StatusNotFound, status, action)
			}
			status, _ = srv.request(t, http.MethodDelete, workspacePath(id), other.session, nil)
			assert.Equal(t, http.StatusNotFound, status)
		}
		status, body := srv.get(t, workspacePath("not-a-uuid"), f.dev.session)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "WORKSPACE_NOT_FOUND", body["type"])
	})

	t.Run("start, stop, delete", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "life", "topic", map[string]string{"life.txt": "x\n"})
		id := createWorkspace(t, f, f.dev, "life")
		status, body := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "starting", body["state"])
		status, body = srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "starting twice is fine: %v", body)

		status, body = srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "WORKSPACE_ACTIVE", body["type"])

		status, body = srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "stopped", body["state"])
		status, _ = srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		assert.Equal(t, http.StatusOK, status, "stopping twice is fine")

		status, _ = srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, workspacePath(id), f.dev.session)
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestWorkspacesOffWithoutDomain(t *testing.T) {
	f := newPullFixture(t, false)
	status, body := f.srv.post(t, repoAPI("owner/app")+"/workspaces", f.dev.session, map[string]any{})
	assert.Equal(t, http.StatusServiceUnavailable, status)
	assert.Equal(t, "WORKSPACES_DISABLED", body["type"])

	f.srv.deps.WorkspaceApp.Config.Workspaces.Domain = "workspaces.test"
	id := createWorkspace(t, f, f.dev, "")
	status, body = f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	f.srv.deps.WorkspaceApp.Config.Workspaces.Domain = ""

	status, body = f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusServiceUnavailable, status, "%v", body)
	status, body = f.srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status, "turned off, people can still clean up: %v", body)
	status, _ = f.srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
	assert.Equal(t, http.StatusOK, status)
}

func TestWorkspaceAccessIsCheckedAtStart(t *testing.T) {
	f := newPullFixture(t, true, withWorkspaces)
	srv := f.srv
	id := createWorkspace(t, f, f.dev, "")
	status, body := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)

	status, body = srv.get(t, repoAPI("owner/app")+"/members", f.owner.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	var member string
	for _, m := range body["members"].([]any) {
		if m.(map[string]any)["username"] == "dev" {
			member = m.(map[string]any)["id"].(string)
		}
	}
	require.NotEmpty(t, member)
	status, body = srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/members/"+member, f.owner.session, map[string]any{"role": "reporter"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusForbidden, status, "developers and up")

	status, body = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"/members/"+member, f.owner.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusNotFound, status, "no read access")
	status, body = srv.get(t, "/api/v1/user/workspaces", f.dev.session)
	require.Equal(t, http.StatusOK, status)
	workspaces := body["workspaces"].([]any)
	require.Len(t, workspaces, 1)
	assert.Empty(t, workspaces[0].(map[string]any)["repository"], "the private path stays hidden")
	status, _ = srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
	assert.Equal(t, http.StatusOK, status, "their own workspace is still theirs to delete")
}

func TestWorkspacesRefuseReadOnlyRepositories(t *testing.T) {
	f := newPullFixture(t, false, withWorkspaces)
	id := createWorkspace(t, f, f.dev, "")
	status, body := f.srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": true})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "REPOSITORY_ARCHIVED", body["type"])
	status, body = f.srv.post(t, repoAPI("owner/app")+"/workspaces", f.maint.session, map[string]any{})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "REPOSITORY_ARCHIVED", body["type"])

	require.NoError(t, f.srv.deps.Database.Db.Exec("UPDATE repository SET is_archived = FALSE, is_mirror = TRUE").Error)
	status, body = f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "REPOSITORY_MIRROR", body["type"])
}

// Concurrent starts of different workspaces never exceed the limit, on
// Postgres too (OGIT_TEST_DB), where it takes the lock on the user's row.
func TestWorkspaceLimit(t *testing.T) {
	f := newPullFixture(t, false, withWorkspaces, func(cfg *config.Config) { cfg.Workspaces.MaxRunning = 2 })
	var ids []string
	for i := range 8 {
		branch := fmt.Sprintf("b%d", i)
		pushFiles(t, f.url, f.dev.git, branch, "topic", map[string]string{branch: "x\n"})
		ids = append(ids, createWorkspace(t, f, f.dev, branch))
	}
	for round := range 5 {
		var wg sync.WaitGroup
		codes := make([]int, len(ids))
		for i, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i], _ = f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
			}()
		}
		wg.Wait()
		started := 0
		for _, code := range codes {
			if code == http.StatusOK {
				started++
			} else {
				assert.Equal(t, http.StatusConflict, code)
			}
		}
		assert.Equal(t, 2, started, "round %d", round)
		for _, id := range ids {
			status, _ := f.srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
			require.Equal(t, http.StatusOK, status)
		}
	}
	// Directly, without HTTP between the starts: the race is much narrower
	// than a request.
	status, body := f.srv.get(t, "/api/v1/user/profile", f.dev.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	userId := body["id"].(string)
	pers := persistence.NewWorkspacePers(f.srv.deps.Database.Db)
	for round := range 100 {
		var wg sync.WaitGroup
		var started atomic.Int32
		for _, id := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := pers.Start(userId, id, 2, time.Now().UTC())
				assert.NoError(t, err)
				if ok {
					started.Add(1)
				}
			}()
		}
		wg.Wait()
		require.Equal(t, int32(2), started.Load(), "round %d", round)
		require.NoError(t, f.srv.deps.Database.Db.Exec("UPDATE workspace SET state = 'stopped'").Error)
	}

	status, body = f.srv.post(t, workspacePath(ids[0], "start"), f.maint.session, nil)
	assert.Equal(t, http.StatusNotFound, status, "%v", body)

	status, _ = f.srv.post(t, workspacePath(ids[0], "start"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status)
	status, _ = f.srv.post(t, workspacePath(ids[1], "start"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status)
	status, body = f.srv.post(t, workspacePath(ids[2], "start"), f.dev.session, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "WORKSPACE_LIMIT", body["type"])
	other := createWorkspace(t, f, f.maint, "b0")
	status, _ = f.srv.post(t, workspacePath(other, "start"), f.maint.session, nil)
	assert.Equal(t, http.StatusOK, status, "the limit is per user")
}

func TestWorkspacesGoWithRepositoryAndUser(t *testing.T) {
	f := newPullFixture(t, false, withWorkspaces)
	srv := f.srv
	count := func(where string, args ...any) int64 {
		var n int64
		require.NoError(t, srv.deps.Database.Db.Table("workspace").Where(where, args...).Count(&n).Error)
		return n
	}
	createWorkspace(t, f, f.dev, "")
	createWorkspace(t, f, f.maint, "")
	require.Equal(t, int64(2), count("1 = 1"))

	status, body := srv.get(t, "/api/v1/user/profile", f.maint.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	require.NoError(t, persistence.NewUserPers(srv.deps.Database.Db).Purge(body["id"].(string)))
	assert.Equal(t, int64(1), count("1 = 1"), "deleted with their user")

	status, body = srv.request(t, http.MethodDelete, repoAPI("owner/app")+"?confirm=app", f.owner.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Equal(t, int64(0), count("1 = 1"), "deleted with their repository")
}
