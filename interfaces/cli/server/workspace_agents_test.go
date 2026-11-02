package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/persistence"
	"github.com/labbs/ogit/infrastructure/wsagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const logSecretProbe = "ogitw_"

// tryClone reports whether auth can read the repository.
func tryClone(t *testing.T, url string, auth *githttp.BasicAuth) error {
	t.Helper()
	_, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: auth, ReferenceName: "refs/heads/main", SingleBranch: true})
	return err
}

// tryPush reports whether auth can push a new branch.
func tryPush(t *testing.T, url string, auth *githttp.BasicAuth) error {
	t.Helper()
	dir := t.TempDir()
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: url, Auth: auth, ReferenceName: "refs/heads/main", SingleBranch: true})
	require.NoError(t, err)
	return repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{"refs/heads/main:refs/heads/tried-push"}})
}

// fakeRuntime is Docker for agents in tests.
type fakeRuntime struct {
	mu      sync.Mutex
	running map[string]bool
	tasks   []application.WorkspaceTask
	removed []string
	fail    error
	leak    bool // fail with the token in the message
	over    []string
	editor  int
}

func (f *fakeRuntime) Running(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []string{}
	for id := range f.running {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeRuntime) Start(_ context.Context, task application.WorkspaceTask) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, task)
	if f.leak {
		return errors.New("fatal: bad token " + task.Token)
	}
	if f.fail != nil {
		return f.fail
	}
	f.running[task.Id] = true
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, id)
	return nil
}

func (f *fakeRuntime) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.running, id)
	f.removed = append(f.removed, id)
	return nil
}

func (f *fakeRuntime) OverDisk(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.over, nil
}

func (f *fakeRuntime) Address(_ context.Context, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running[id] {
		return "", errors.New("not running")
	}
	return "127.0.0.1", nil
}

func (f *fakeRuntime) EditorPort() int { return f.editor }

func (f *fakeRuntime) lastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks[len(f.tasks)-1].Token
}

type testAgent struct {
	*wsagent.Agent
	id      string
	runtime *fakeRuntime
}

// sync does one round and lets its tasks finish.
func (a *testAgent) sync(t *testing.T) {
	t.Helper()
	require.NoError(t, a.Sync(context.Background()))
	a.Settle()
}

func (s *testServer) agentToken(t *testing.T, admin user, body map[string]any) string {
	t.Helper()
	status, resp := s.post(t, "/api/v1/admin/workspace-agents/registration-token", admin.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	return resp["token"].(string)
}

func (s *testServer) newAgent(t *testing.T, admin user, name string) *testAgent {
	t.Helper()
	token := s.agentToken(t, admin, map[string]any{})
	reg, err := (&wsagent.Client{URL: s.url}).Register(context.Background(), token, name, "test")
	require.NoError(t, err)
	runtime := &fakeRuntime{running: map[string]bool{}}
	return &testAgent{id: reg.Id, runtime: runtime, Agent: &wsagent.Agent{Client: &wsagent.Client{URL: s.url, Token: reg.Token}, Runtime: runtime, Version: "test", MaxWorkspaces: 10}}
}

type agentFixture struct {
	*pullFixture
	admin user
	agent *testAgent
}

func newAgentFixture(t *testing.T) *agentFixture {
	f := &agentFixture{pullFixture: newPullFixture(t, true, withWorkspaces)}
	f.admin = f.srv.newUser(t, "root")
	f.srv.promote(t, "root")
	f.agent = f.srv.newAgent(t, f.admin, "one")
	return f
}

func (f *agentFixture) state(t *testing.T, as user, id string) map[string]any {
	t.Helper()
	status, body := f.srv.get(t, workspacePath(id), as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

// running creates and starts a workspace of dev's, through the agent.
func (f *agentFixture) running(t *testing.T, branch string) string {
	t.Helper()
	pushFiles(t, f.url, f.dev.git, branch, "work", map[string]string{branch + ".txt": "x\n"})
	id := createWorkspace(t, f.pullFixture, f.dev, branch)
	status, body := f.srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	f.agent.sync(t)
	f.agent.sync(t)
	require.Equal(t, "running", f.state(t, f.dev, id)["state"])
	return id
}

func workspaceAuth(token string) *githttp.BasicAuth {
	return &githttp.BasicAuth{Username: "workspace", Password: token}
}

func TestWorkspaceAgentRegistration(t *testing.T) {
	f := newAgentFixture(t)
	srv := f.srv
	ctx := context.Background()
	client := &wsagent.Client{URL: srv.url}
	path := "/api/v1/admin/workspace-agents"

	t.Run("admins only", func(t *testing.T) {
		status, _ := srv.post(t, path+"/registration-token", f.owner.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, path, f.owner.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, "/api/v1/admin/workspaces", f.owner.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, path+"/registration-tokens", f.owner.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, path+"/"+f.agent.id, f.owner.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, path+"/registration-tokens/"+f.agent.id, f.owner.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("one-time tokens work once", func(t *testing.T) {
		token := srv.agentToken(t, f.admin, map[string]any{})
		_, err := client.Register(ctx, token, "a", "1")
		require.NoError(t, err)
		_, err = client.Register(ctx, token, "b", "1")
		assert.Error(t, err)
		_, err = client.Register(ctx, "ogitwreg_wrong", "c", "1")
		assert.Error(t, err)
	})

	t.Run("reusable tokens", func(t *testing.T) {
		status, _ := srv.post(t, path+"/registration-token", f.admin.session, map[string]any{"expires_at": time.Now().Add(time.Hour)})
		assert.Equal(t, http.StatusBadRequest, status, "only reusable tokens take an expiry")
		status, _ = srv.post(t, path+"/registration-token", f.admin.session, map[string]any{"reusable": true, "expires_at": time.Now().Add(-time.Hour)})
		assert.Equal(t, http.StatusBadRequest, status)

		srv.agentToken(t, f.admin, map[string]any{}) // one-time tokens aren't listed
		token := srv.agentToken(t, f.admin, map[string]any{"reusable": true, "name": "fleet"})
		a, err := client.Register(ctx, token, "a", "1")
		require.NoError(t, err)
		_, err = client.Register(ctx, token, "b", "1")
		require.NoError(t, err)
		status, body := srv.get(t, path+"/registration-tokens", f.admin.session)
		require.Equal(t, http.StatusOK, status)
		tokens := body["tokens"].([]any)
		require.Len(t, tokens, 1)
		listed := tokens[0].(map[string]any)
		assert.Equal(t, float64(2), listed["agents"])
		assert.NotContains(t, listed, "token")

		status, _ = srv.request(t, http.MethodDelete, path+"/registration-tokens/"+listed["id"].(string), f.admin.session, nil)
		require.Equal(t, http.StatusOK, status)
		_, err = client.Register(ctx, token, "c", "1")
		assert.Error(t, err, "revoked")
		_, err = (&wsagent.Client{URL: srv.url, Token: a.Token}).Sync(ctx, application.AgentReport{})
		assert.NoError(t, err, "revoking keeps the agents")
	})

	t.Run("expired tokens don't register", func(t *testing.T) {
		token := srv.agentToken(t, f.admin, map[string]any{"reusable": true, "expires_at": time.Now().Add(time.Hour)})
		require.NoError(t, srv.deps.Database.Db.Model(&domain.WorkspaceAgentToken{}).Where("token_hash = ?", application.HashToken(token)).
			Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error)
		_, err := client.Register(ctx, token, "late", "1")
		assert.Error(t, err)
	})

	t.Run("a token is needed to sync", func(t *testing.T) {
		_, err := (&wsagent.Client{URL: srv.url, Token: "ogita_wrong"}).Sync(ctx, application.AgentReport{})
		assert.ErrorIs(t, err, wsagent.ErrUnregistered)
		_, err = (&wsagent.Client{URL: srv.url, Token: f.dev.git.Password}).Sync(ctx, application.AgentReport{})
		assert.ErrorIs(t, err, wsagent.ErrUnregistered, "other tokens aren't agent tokens")
	})

	t.Run("deleted agents stop", func(t *testing.T) {
		id := f.running(t, "gone")
		status, body := srv.request(t, http.MethodDelete, path+"/"+f.agent.id, f.admin.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.ErrorIs(t, f.agent.Run(ctx), wsagent.ErrUnregistered)
		state := f.state(t, f.dev, id)
		assert.Equal(t, "stopped", state["state"])
		assert.Equal(t, "its agent was deleted", state["failure"])
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "agent_id IS NOT NULL"))
	})
}

func TestWorkspaceAgentLifecycle(t *testing.T) {
	f := newAgentFixture(t)
	srv, agent := f.srv, f.agent

	id := f.running(t, "life")
	require.Len(t, agent.runtime.tasks, 1)
	task := agent.runtime.tasks[0]
	assert.Equal(t, "owner/app.git", task.Repository)
	assert.Equal(t, "life", task.Branch)
	assert.Equal(t, srv.deps.Config.Workspaces.Image, task.Image)
	token := task.Token

	t.Run("the token is stored hashed", func(t *testing.T) {
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "token_hash = ?", token))
		assert.Equal(t, int64(1), srv.countRows(t, &domain.Workspace{}, "token_hash = ?", application.HashToken(token)))
	})

	t.Run("the token is the user, on that repository", func(t *testing.T) {
		pushFiles(t, f.url, workspaceAuth(token), "from-workspace", "from the workspace", map[string]string{"w.txt": "w\n"})
		assert.Equal(t, "w\n", gitFiles(t, f.url, user{git: workspaceAuth(token)}, "from-workspace")["w.txt"])

		srv.createRepo(t, f.dev.session, "Mine", true)
		other := srv.url + "/dev/mine.git"
		pushFiles(t, other, f.dev.git, "main", "base", map[string]string{"a": "a\n"})
		assert.Error(t, tryClone(t, other, workspaceAuth(token)), "another repository of the same user")

		status, body := srv.get(t, "/api/v1/user/profile", token)
		assert.NotEqual(t, http.StatusOK, status, "git only: %v", body)
	})

	t.Run("pushes follow the user's role", func(t *testing.T) {
		id := srv.memberID(t, "owner/app", f.owner, "dev")
		status, body := srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/members/"+id, f.owner.session, map[string]any{"role": "reporter"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Error(t, tryPush(t, f.url, workspaceAuth(token)), "reporters can't push")
		assert.NoError(t, tryClone(t, f.url, workspaceAuth(token)), "they can read")
		status, body = srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/members/"+id, f.owner.session, map[string]any{"role": "developer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
	})

	t.Run("stopping kills the token at once", func(t *testing.T) {
		status, body := srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "stopping", body["state"])
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)))
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "id = ? AND token_hash IS NOT NULL", id))
		status, _ = srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
		assert.Equal(t, http.StatusConflict, status, "not stopped yet")

		agent.sync(t)
		assert.Empty(t, agent.runtime.running, "the container is gone")
		assert.Equal(t, "stopping", f.state(t, f.dev, id)["state"], "it was running when the agent reported")
		agent.sync(t)
		assert.Equal(t, "stopped", f.state(t, f.dev, id)["state"])
		assert.Empty(t, agent.runtime.removed, "the volume stays")
	})

	t.Run("a restart gets a new token, on the same agent", func(t *testing.T) {
		other := srv.newAgent(t, f.admin, "two")
		status, body := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		other.sync(t)
		assert.Empty(t, other.runtime.tasks, "the volume is on the first agent")
		agent.sync(t)
		agent.sync(t)
		assert.Equal(t, "running", f.state(t, f.dev, id)["state"])
		assert.NotEqual(t, token, agent.runtime.lastToken())
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)), "the old token")
		assert.NoError(t, tryClone(t, f.url, workspaceAuth(agent.runtime.lastToken())))
	})

	t.Run("deleting removes the volume", func(t *testing.T) {
		status, _ := srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.sync(t)
		agent.sync(t)
		status, _ = srv.request(t, http.MethodDelete, workspacePath(id), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, int64(1), srv.countRows(t, &domain.WorkspaceCleanup{}, "workspace_id = ?", id))
		agent.sync(t)
		assert.Equal(t, []string{id}, agent.runtime.removed)
		agent.sync(t)
		assert.Zero(t, srv.countRows(t, &domain.WorkspaceCleanup{}, "workspace_id = ?", id), "acknowledged")
	})

	t.Run("a failed start says why, without the token", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "bad", "work", map[string]string{"b.txt": "x\n"})
		id := createWorkspace(t, f.pullFixture, f.dev, "bad")
		status, _ := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.runtime.fail = errors.New("boom")
		agent.sync(t)
		agent.sync(t)
		state := f.state(t, f.dev, id)
		assert.Equal(t, "stopped", state["state"])
		assert.Equal(t, "boom", state["failure"])

		status, _ = srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.runtime.leak = true
		agent.sync(t)
		agent.sync(t)
		agent.runtime.leak = false
		state = f.state(t, f.dev, id)
		assert.NotContains(t, state["failure"], logSecretProbe)
		assert.Contains(t, state["failure"], "[token]")
	})

	t.Run("a workspace over its disk stops", func(t *testing.T) {
		agent.runtime.fail = nil
		id := f.running(t, "big")
		agent.runtime.over = []string{id}
		agent.DiskEvery = time.Nanosecond
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = agent.Run(ctx)
		state := f.state(t, f.dev, id)
		assert.Equal(t, "stopped", state["state"])
		assert.Equal(t, "over its disk limit", state["failure"])
		assert.Empty(t, agent.runtime.running)
	})
}

func TestWorkspaceAgentsShareWork(t *testing.T) {
	f := newAgentFixture(t)
	srv := f.srv
	srv.deps.WorkspaceApp.Config.Workspaces.MaxRunning = 20
	srv.deps.WorkspaceApp.Config.Workspaces.MaxTotal = 20

	var ids []string
	for _, branch := range []string{"a", "b", "c", "d", "e", "f"} {
		pushFiles(t, f.url, f.dev.git, branch, "work", map[string]string{branch: "x\n"})
		id := createWorkspace(t, f.pullFixture, f.dev, branch)
		status, _ := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		ids = append(ids, id)
	}

	t.Run("each goes to one agent", func(t *testing.T) {
		agents := []*testAgent{f.agent, srv.newAgent(t, f.admin, "two"), srv.newAgent(t, f.admin, "three")}
		var wg sync.WaitGroup
		for _, a := range agents {
			a.MaxWorkspaces = 3
			wg.Add(1)
			go func() {
				defer wg.Done()
				a.sync(t)
			}()
		}
		wg.Wait()
		var given []string
		for _, a := range agents {
			assert.LessOrEqual(t, len(a.runtime.tasks), 3, "its free slots")
			for _, task := range a.runtime.tasks {
				given = append(given, task.Id)
			}
		}
		slices.Sort(given)
		assert.Equal(t, len(slices.Compact(slices.Clone(given))), len(given), "%v", given)
	})

	t.Run("agents only report on their own workspaces", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "own", "work", map[string]string{"own": "x\n"})
		id := createWorkspace(t, f.pullFixture, f.dev, "own")
		status, _ := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		var holder string
		for range 10 {
			for _, a := range []*testAgent{f.agent} {
				a.MaxWorkspaces = 20
				a.sync(t)
			}
			state := f.state(t, f.dev, id)
			if state["state"] == "running" {
				holder = "one"
				break
			}
		}
		require.Equal(t, "one", holder)
		other := srv.newAgent(t, f.admin, "stranger")
		ctx := context.Background()
		tasks, err := other.Client.Sync(ctx, application.AgentReport{Running: []string{id}, Failed: []application.AgentFailure{{Id: id, Message: "forged"}}})
		require.NoError(t, err)
		assert.Equal(t, []string{id}, tasks.Stop, "not its workspace: its container should go")
		state := f.state(t, f.dev, id)
		assert.Equal(t, "running", state["state"])
		assert.Empty(t, state["failure"])
		status, _ = srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		f.agent.sync(t)
		f.agent.sync(t)
		status, _ = srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		_, err = other.Client.Sync(ctx, application.AgentReport{Running: []string{id}})
		require.NoError(t, err)
		assert.Equal(t, "starting", f.state(t, f.dev, id)["state"], "a stranger can't say it runs")
		_, err = other.Client.Sync(ctx, application.AgentReport{Running: []string{"' OR 1=1 --"}, Removed: []string{"nonsense"}})
		assert.NoError(t, err, "ids that aren't ids are ignored")
	})
	_ = ids
}

// Directly, without HTTP between the claims: the race is narrow.
func TestWorkspaceClaimsAreConditional(t *testing.T) {
	f := newAgentFixture(t)
	srv := f.srv
	id := createWorkspace(t, f.pullFixture, f.dev, "main")
	pers := persistence.NewWorkspacePers(srv.deps.Database.Db)
	agents := []string{f.agent.id, srv.newAgent(t, f.admin, "two").id, srv.newAgent(t, f.admin, "three").id, srv.newAgent(t, f.admin, "four").id}
	for round := range 200 {
		require.NoError(t, srv.deps.Database.Db.Exec("UPDATE workspace SET state = 'starting', agent_id = NULL WHERE id = ?", id).Error)
		var wg sync.WaitGroup
		var claimed atomic.Int32
		for _, agent := range agents {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got, err := pers.Claim(agent, 1, time.Now().UTC())
				assert.NoError(t, err)
				claimed.Add(int32(len(got)))
			}()
		}
		wg.Wait()
		require.Equal(t, int32(1), claimed.Load(), "round %d", round)
	}
}

func TestWorkspaceSweep(t *testing.T) {
	f := newAgentFixture(t)
	srv, agent := f.srv, f.agent
	age := func(t *testing.T, model any, column, where string, d time.Duration, args ...any) {
		t.Helper()
		require.NoError(t, srv.deps.Database.Db.Model(model).Where(where, args...).Update(column, time.Now().UTC().Add(-d)).Error)
	}

	t.Run("lost agents", func(t *testing.T) {
		id := f.running(t, "lost")
		token := agent.runtime.lastToken()
		srv.deps.WorkspaceApp.Sweep()
		assert.Equal(t, "running", f.state(t, f.dev, id)["state"], "still seen")
		age(t, &domain.WorkspaceAgent{}, "last_seen_at", "id = ?", 6*time.Minute, agent.id)
		srv.deps.WorkspaceApp.Sweep()
		state := f.state(t, f.dev, id)
		assert.Equal(t, "stopped", state["state"])
		assert.Equal(t, "its agent stopped reporting", state["failure"])
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)))

		status, body := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		assert.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, "WORKSPACE_AGENT_OFFLINE", body["type"])

		agent.sync(t)
		assert.Empty(t, agent.runtime.running, "a returning agent stops what nobody wants")
	})

	t.Run("tokens expire without reports", func(t *testing.T) {
		id := f.running(t, "expire")
		token := agent.runtime.lastToken()
		age(t, &domain.Workspace{}, "token_expires_at", "id = ?", time.Minute, id)
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)))
		agent.sync(t)
		assert.NoError(t, tryClone(t, f.url, workspaceAuth(token)), "a report of the running container extends it")
		status, _ := srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.sync(t)
		agent.sync(t)
	})

	t.Run("nothing starts it", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "stuck", "work", map[string]string{"s": "x\n"})
		id := createWorkspace(t, f.pullFixture, f.dev, "stuck")
		status, _ := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		age(t, &domain.Workspace{}, "last_used_at", "id = ?", 31*time.Minute, id)
		srv.deps.WorkspaceApp.Sweep()
		state := f.state(t, f.dev, id)
		assert.Equal(t, "stopped", state["state"])
		assert.Equal(t, "no agent started it in time", state["failure"])
	})

	retention := 72 * time.Hour
	srv.deps.WorkspaceApp.Config.Workspaces.Retention = retention
	sweepAt := func(t *testing.T, id string, lostAgo time.Duration) {
		t.Helper()
		age(t, &domain.Workspace{}, "access_lost_at", "id = ?", lostAgo, id)
		srv.deps.WorkspaceApp.Sweep()
	}
	kick := func(t *testing.T) {
		t.Helper()
		member := srv.memberID(t, "owner/app", f.owner, "dev")
		status, body := srv.request(t, http.MethodDelete, repoAPI("owner/app")+"/members/"+member, f.owner.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	invite := func(t *testing.T) {
		t.Helper()
		status, body := srv.post(t, repoAPI("owner/app")+"/members", f.owner.session, map[string]any{"username": "dev", "role": "developer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	setActive := func(t *testing.T, active bool) {
		t.Helper()
		status, body := srv.request(t, http.MethodPatch, "/api/v1/admin/users/dev", f.admin.session, map[string]any{"active": active})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	row := func(t *testing.T, id string) domain.Workspace {
		t.Helper()
		var w domain.Workspace
		require.NoError(t, srv.deps.Database.Db.First(&w, "id = ?", id).Error)
		return w
	}
	settled := func(t *testing.T, id string) {
		t.Helper()
		agent.sync(t)
		agent.sync(t)
		assert.NotContains(t, agent.runtime.running, id, "the container stops")
		assert.NotContains(t, agent.runtime.removed, id, "its files stay")
		assert.Equal(t, domain.WorkspaceStopped, row(t, id).State)
		assert.Equal(t, "its user lost access", row(t, id).Failure)
	}

	t.Run("losing access stops it, and deletes it after the retention", func(t *testing.T) {
		id := f.running(t, "kicked")
		token := agent.runtime.lastToken()
		kick(t)
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)), "at once")
		srv.deps.WorkspaceApp.Sweep()
		w := row(t, id)
		assert.Nil(t, w.TokenHash, "the token goes at once")
		assert.Nil(t, w.TokenExpiresAt)
		require.NotNil(t, w.AccessLostAt)
		settled(t, id)

		status, _ := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		assert.Equal(t, http.StatusNotFound, status, "the private repository stays hidden")
		assert.Equal(t, domain.WorkspaceStopped, row(t, id).State)

		mark := *row(t, id).AccessLostAt
		srv.deps.WorkspaceApp.Sweep()
		assert.WithinDuration(t, mark, *row(t, id).AccessLostAt, time.Second, "the clock starts once")
		marked, err := srv.deps.WorkspaceApp.Workspaces.MarkAccessLost(id, "another server", mark.Add(time.Hour))
		require.NoError(t, err)
		assert.False(t, marked, "servers sweeping together mark it once")
		assert.WithinDuration(t, mark, *row(t, id).AccessLostAt, time.Second)

		sweepAt(t, id, retention-time.Hour)
		assert.EqualValues(t, 1, srv.countRows(t, &domain.Workspace{}, "id = ?", id), "kept until the retention ends")
		sweepAt(t, id, retention+time.Minute)
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "id = ?", id))
		agent.sync(t)
		assert.Contains(t, agent.runtime.removed, id)
		invite(t)
	})

	t.Run("getting access back keeps it", func(t *testing.T) {
		id := f.running(t, "returns")
		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		settled(t, id)
		sweepAt(t, id, retention-time.Hour)
		invite(t)
		sweepAt(t, id, retention+time.Hour)
		w := row(t, id)
		assert.Nil(t, w.AccessLostAt)
		assert.Empty(t, w.Failure)

		status, body := srv.post(t, workspacePath(id, "start"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		agent.sync(t)
		assert.NotEmpty(t, agent.runtime.lastToken())
		assert.NotNil(t, row(t, id).TokenHash, "a new token")
		status, _ = srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.sync(t)
		agent.sync(t)

		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		srv.deps.WorkspaceApp.Sweep()
		assert.EqualValues(t, 1, srv.countRows(t, &domain.Workspace{}, "id = ?", id), "losing it again starts the clock again")
		invite(t)
	})

	t.Run("other failures survive getting access back", func(t *testing.T) {
		id := f.running(t, "other-failure")
		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		agent.sync(t)
		agent.sync(t)
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Workspace{}).Where("id = ?", id).Update("failure", "something else").Error)
		invite(t)
		srv.deps.WorkspaceApp.Sweep()
		assert.Equal(t, "something else", row(t, id).Failure)
		assert.Nil(t, row(t, id).AccessLostAt)
	})

	t.Run("a stopped workspace is marked too", func(t *testing.T) {
		id := f.running(t, "was-stopped")
		status, _ := srv.post(t, workspacePath(id, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		agent.sync(t)
		agent.sync(t)
		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		assert.NotNil(t, row(t, id).AccessLostAt)
		invite(t)
		srv.deps.WorkspaceApp.Sweep()
	})

	t.Run("one started as access went stops", func(t *testing.T) {
		id := f.running(t, "raced")
		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		agent.sync(t)
		agent.sync(t)
		hash := "raced"
		require.NoError(t, srv.deps.Database.Db.Model(&domain.Workspace{}).Where("id = ?", id).
			Updates(map[string]any{"state": domain.WorkspaceRunning, "token_hash": hash}).Error)
		sweepAt(t, id, time.Minute)
		assert.Nil(t, row(t, id).TokenHash)
		assert.NotEqual(t, domain.WorkspaceRunning, row(t, id).State)
		invite(t)
		srv.deps.WorkspaceApp.Sweep()
		agent.sync(t)
		agent.sync(t)
	})

	t.Run("without a retention setting it's 14 days", func(t *testing.T) {
		srv.deps.WorkspaceApp.Config.Workspaces.Retention = 0
		defer func() { srv.deps.WorkspaceApp.Config.Workspaces.Retention = retention }()
		id := f.running(t, "default-retention")
		kick(t)
		srv.deps.WorkspaceApp.Sweep()
		agent.sync(t)
		agent.sync(t)
		sweepAt(t, id, 13*24*time.Hour)
		assert.EqualValues(t, 1, srv.countRows(t, &domain.Workspace{}, "id = ?", id))
		sweepAt(t, id, 15*24*time.Hour)
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "id = ?", id))
		invite(t)
		agent.sync(t)
	})

	t.Run("disabled users lose it the same way", func(t *testing.T) {
		id := f.running(t, "disabled")
		token := agent.runtime.lastToken()
		setActive(t, false)
		assert.Error(t, tryClone(t, f.url, workspaceAuth(token)), "at once")
		srv.deps.WorkspaceApp.Sweep()
		assert.Nil(t, row(t, id).TokenHash)
		settled(t, id)
		setActive(t, true)
		srv.deps.WorkspaceApp.Sweep()
		assert.Nil(t, row(t, id).AccessLostAt, "enabling them again keeps it")

		setActive(t, false)
		srv.deps.WorkspaceApp.Sweep()
		sweepAt(t, id, retention+time.Minute)
		assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "id = ?", id))
		setActive(t, true)
	})
}

func TestWorkspaceAdmin(t *testing.T) {
	f := newAgentFixture(t)
	srv, agent := f.srv, f.agent
	id := f.running(t, "admin")

	status, body := srv.get(t, "/api/v1/admin/workspaces", f.admin.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	listed := body["workspaces"].([]any)
	require.Len(t, listed, 1)
	assert.Equal(t, "owner/app", listed[0].(map[string]any)["repository"])
	assert.Equal(t, agent.id, listed[0].(map[string]any)["agent_id"])

	for _, path := range []string{"/api/v1/admin/workspaces/" + id + "/stop"} {
		status, _ = srv.post(t, path, f.owner.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
	}
	status, _ = srv.request(t, http.MethodDelete, "/api/v1/admin/workspaces/"+id, f.owner.session, nil)
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = srv.post(t, "/api/v1/admin/workspaces/nonsense/stop", f.admin.session, nil)
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = srv.post(t, "/api/v1/admin/workspaces/"+id+"/stop", f.admin.session, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "stopping", f.state(t, f.dev, id)["state"])
	agent.sync(t)
	agent.sync(t)
	assert.Equal(t, "stopped", f.state(t, f.dev, id)["state"])

	other := f.running(t, "admin2")
	status, body = srv.request(t, http.MethodDelete, "/api/v1/admin/workspaces/"+other, f.admin.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	agent.sync(t)
	assert.Contains(t, agent.runtime.removed, other, "running ones too")
}

func TestWorkspaceCleanupsOutliveRows(t *testing.T) {
	f := newAgentFixture(t)
	srv, agent := f.srv, f.agent
	id := f.running(t, "purged")
	status, body := srv.request(t, http.MethodDelete, repoAPI("owner/app")+"?confirm=app", f.owner.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Zero(t, srv.countRows(t, &domain.Workspace{}, "id = ?", id))
	agent.sync(t)
	assert.Equal(t, []string{id}, agent.runtime.removed, "the repository's deletion reached the agent")
	assert.Empty(t, agent.runtime.running)
}

func TestWorkspaceCleanupsOutliveUsers(t *testing.T) {
	f := newAgentFixture(t)
	id := f.running(t, "purged")
	status, body := f.srv.request(t, http.MethodDelete, "/api/v1/admin/users/dev", f.admin.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
	f.agent.sync(t)
	assert.Equal(t, []string{id}, f.agent.runtime.removed)
}

func TestWorkspaceTotalLimit(t *testing.T) {
	f := newPullFixture(t, true, withWorkspaces, func(cfg *config.Config) { cfg.Workspaces.MaxTotal = 2 })
	for _, branch := range []string{"a", "b", "c"} {
		pushFiles(t, f.url, f.dev.git, branch, "work", map[string]string{branch: "x\n"})
	}
	createWorkspace(t, f, f.dev, "a")
	createWorkspace(t, f, f.dev, "b")
	status, body := f.srv.post(t, repoAPI("owner/app")+"/workspaces", f.dev.session, map[string]any{"branch": "c"})
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, "WORKSPACE_TOTAL", body["type"])
	createWorkspace(t, f, f.maint, "c")
}
