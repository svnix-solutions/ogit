package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *testServer) fork(t *testing.T, as user, repo string, body map[string]any) map[string]any {
	t.Helper()
	status, resp := s.post(t, repoAPI(repo)+"/forks", as.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	return resp
}

// refs returns a repository's branches and tags with their commits.
func (s *testServer) refs(t *testing.T, as user, repo string) map[string]string {
	t.Helper()
	status, body := s.get(t, repoAPI(repo)+"/refs", as.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	out := map[string]string{}
	for _, kind := range []string{"branches", "tags"} {
		for _, r := range body[kind].([]any) {
			r := r.(map[string]any)
			out[kind+"/"+r["name"].(string)] = r["commit"].(map[string]any)["hash"].(string)
		}
	}
	out["default"] = body["default_branch"].(string)
	return out
}

func TestForks(t *testing.T) {
	t.Run("local", func(t *testing.T) { testForks(t) })
	t.Run("s3_fake", func(t *testing.T) { testForks(t, withS3(s3test.StartFake(t))) })
}

func testForks(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	alice, bob, carol := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol")
	srv.createRepo(t, alice.session, "App", false)
	url := srv.url + "/alice/app.git"
	pushFiles(t, url, alice.git, "main", "v1", map[string]string{"app.txt": "v1\n"})
	pushFiles(t, url, alice.git, "dev", "wip", map[string]string{"dev.txt": "wip\n"})
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: alice.git})
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	_, err = repo.CreateTag("v1.0", head.Hash(), nil)
	require.NoError(t, err)
	require.NoError(t, repo.Push(&git.PushOptions{Auth: alice.git, RefSpecs: []gitconfig.RefSpec{"refs/tags/v1.0:refs/tags/v1.0"}}))

	f := srv.fork(t, bob, "alice/app", map[string]any{})
	assert.Equal(t, "bob/app", f["full_path"])
	assert.Equal(t, true, f["is_fork"])
	assert.Equal(t, "alice/app", f["fork_of"].(map[string]any)["full_path"])
	assert.Equal(t, false, f["is_private"], "a public repository's fork is public")
	assert.Equal(t, false, f["has_issues"], "issues start off, as on GitHub")
	assert.Equal(t, srv.refs(t, alice, "alice/app"), srv.refs(t, bob, "bob/app"), "every branch and tag, and the default branch")
	_, err = clone(t, srv.url+"/bob/app.git", bob.git)
	require.NoError(t, err, "a working git repository")

	t.Run("listed_and_named", func(t *testing.T) {
		_, body := srv.get(t, repoAPI("alice/app")+"/forks", "")
		assert.Equal(t, "bob/app", body["forks"].([]any)[0].(map[string]any)["full_path"])
		status, body := srv.post(t, repoAPI("alice/app")+"/forks", bob.session, map[string]any{})
		assert.Equal(t, http.StatusConflict, status, "the name is taken: %v", body)
		srv.fork(t, bob, "alice/app", map[string]any{"name": "App 2"})
		status, _ = srv.post(t, repoAPI("alice/app")+"/forks", "", map[string]any{})
		assert.NotEqual(t, http.StatusOK, status, "signed in only")
	})

	t.Run("sync", func(t *testing.T) {
		next := pushOnto(t, url, alice.git, "main", "v2", map[string]string{"app.txt": "v2\n"})
		status, _ := srv.post(t, repoAPI("bob/app")+"/sync", carol.session, map[string]any{})
		assert.Equal(t, http.StatusForbidden, status, "writers of the fork")
		status, body := srv.post(t, repoAPI("bob/app")+"/sync", bob.session, map[string]any{})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["updated"])
		assert.Equal(t, next, body["after"])
		assert.Equal(t, next, srv.refs(t, bob, "bob/app")["branches/main"])
		_, body = srv.post(t, repoAPI("bob/app")+"/sync", bob.session, map[string]any{})
		assert.Equal(t, false, body["updated"], "up to date")

		pushOnto(t, srv.url+"/bob/app.git", bob.git, "main", "mine", map[string]string{"bob.txt": "b\n"})
		pushOnto(t, url, alice.git, "main", "v3", map[string]string{"app.txt": "v3\n"})
		status, body = srv.post(t, repoAPI("bob/app")+"/sync", bob.session, map[string]any{})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "DIVERGED", body["type"])

		status, body = srv.post(t, repoAPI("bob/app")+"/sync", bob.session, map[string]any{"branch": "dev"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, false, body["updated"])
		status, body = srv.post(t, repoAPI("alice/app")+"/sync", alice.session, map[string]any{})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "NOT_A_FORK", body["type"])
	})

	t.Run("independent_of_the_original", func(t *testing.T) {
		srv.createRepo(t, alice.session, "Tmp", false)
		pushFiles(t, srv.url+"/alice/tmp.git", alice.git, "main", "t", map[string]string{"t.txt": "t\n"})
		srv.fork(t, bob, "alice/tmp", map[string]any{})
		status, _ := srv.request(t, http.MethodDelete, repoAPI("alice/tmp")+"?confirm=tmp", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, body := srv.get(t, repoAPI("bob/tmp"), bob.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["is_fork"], "no longer a fork")
		_, err := clone(t, srv.url+"/bob/tmp.git", bob.git)
		assert.NoError(t, err)
	})
}

func TestPrivateForks(t *testing.T) {
	srv := startTestServer(t)
	alice, bob, carol := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol")
	srv.createRepo(t, alice.session, "Secret", true)
	pushFiles(t, srv.url+"/alice/secret.git", alice.git, "main", "s", map[string]string{"s.txt": "s\n"})
	status, _ := srv.post(t, repoAPI("alice/secret")+"/forks", bob.session, map[string]any{})
	assert.Equal(t, http.StatusNotFound, status, "only readers fork, and it stays hidden")

	status, _ = srv.post(t, repoAPI("alice/secret")+"/members", alice.session, map[string]any{"username": "bob", "role": "reporter"})
	require.Equal(t, http.StatusOK, status)
	f := srv.fork(t, bob, "alice/secret", map[string]any{})
	assert.Equal(t, true, f["is_private"], "forks of private repositories are private")

	status, _ = srv.get(t, repoAPI("bob/secret"), alice.session)
	assert.Equal(t, http.StatusNotFound, status, "the original's owner gets no access to the fork")
	_, body := srv.get(t, repoAPI("alice/secret")+"/forks", alice.session)
	assert.Empty(t, body["forks"], "nor sees it listed")
	_, body = srv.get(t, repoAPI("alice/secret")+"/forks", bob.session)
	assert.Len(t, body["forks"], 1)

	// Losing access to the original: the fork stays, without a visible parent.
	members := srv.members(t, "alice/secret", alice)
	status, _ = srv.request(t, http.MethodDelete, repoAPI("alice/secret")+"/members/"+members["bob"]["id"].(string), alice.session, nil)
	require.Equal(t, http.StatusOK, status)
	_, body = srv.get(t, repoAPI("bob/secret"), bob.session)
	assert.Equal(t, true, body["is_fork"])
	assert.Nil(t, body["fork_of"], "the original is not disclosed")
	status, _ = srv.post(t, repoAPI("bob/secret")+"/sync", bob.session, map[string]any{})
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = srv.post(t, "/api/v1/group", carol.session, map[string]any{"name": "team"})
	require.Equal(t, http.StatusOK, status)
	srv.createRepo(t, alice.session, "Open", false)
	f = srv.fork(t, carol, "alice/open", map[string]any{"namespace": "team"})
	assert.Equal(t, "team/open", f["full_path"], "into a group the forker maintains")
	status, _ = srv.post(t, repoAPI("alice/open")+"/forks", bob.session, map[string]any{"namespace": "team"})
	assert.NotEqual(t, http.StatusOK, status, "not into someone else's group")
}

func TestPullRequestsFromForks(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPullRequestsFromForks(t) })
	t.Run("s3_fake", func(t *testing.T) { testPullRequestsFromForks(t, withS3(s3test.StartFake(t))) })
}

func testPullRequestsFromForks(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, allowLoopback)...)
	alice, bob, carol, dave := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol"), srv.newUser(t, "dave")
	srv.createRepo(t, alice.session, "App", false)
	status, _ := srv.post(t, repoAPI("alice/app")+"/members", alice.session, map[string]any{"username": "dave", "role": "developer"})
	require.Equal(t, http.StatusOK, status)
	pushFiles(t, srv.url+"/alice/app.git", alice.git, "main", "v1", map[string]string{"app.txt": "v1\n"})
	rcv := newHookReceiver(t)
	srv.createHook(t, alice, "alice/app", map[string]any{"url": rcv.URL, "events": []string{"pull_request", "fork"}})
	srv.deliver(t)
	rcv.take()

	srv.fork(t, bob, "alice/app", map[string]any{})
	srv.deliver(t)
	evs := rcv.takeEvents()
	require.Equal(t, []string{"fork."}, names(evs))
	assert.Equal(t, "bob/app", evs[0].payload["forkee"].(map[string]any)["full_name"])

	forkURL := srv.url + "/bob/app.git"
	head := pushFiles(t, forkURL, bob.git, "fix", "Fix it", map[string]string{"app.txt": "v1 fixed\n"})

	status, cmp := srv.get(t, repoAPI("alice/app")+"/pulls/preview?source_repository=bob/app&source=fix&target=main", bob.session)
	require.Equal(t, http.StatusOK, status, "%v", cmp)
	assert.Len(t, cmp["commits"], 1, "compared across forks, before opening")

	status, body := srv.post(t, pullsAPI("alice/app"), carol.session, map[string]any{"title": "x", "source": "fix", "source_repository": "bob/app"})
	assert.Equal(t, http.StatusForbidden, status, "only writers of the fork: %v", body)
	srv.createRepo(t, bob.session, "Other", false)
	pushFiles(t, srv.url+"/bob/other.git", bob.git, "fix", "x", nil)
	status, body = srv.post(t, pullsAPI("alice/app"), bob.session, map[string]any{"title": "x", "source": "fix", "source_repository": "bob/other"})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "NOT_IN_NETWORK", body["type"])

	status, pr := srv.post(t, pullsAPI("alice/app"), bob.session, map[string]any{"title": "Fix it", "source": "fix", "source_repository": "bob/app"})
	require.Equal(t, http.StatusOK, status, "readers of the target open pull requests from their fork: %v", pr)
	n := pr["number"]
	assert.Equal(t, "bob/app", pr["source_repository"])
	assert.Equal(t, head, pr["head_commit"])
	assert.Equal(t, true, pr["allow_maintainer_edits"], "on by default")
	assert.Equal(t, true, pr["can_edit"], "the author edits it")
	assert.Equal(t, false, pr["can_merge"])
	srv.deliver(t)
	evs = rcv.takeEvents()
	require.Equal(t, []string{"pull_request.opened"}, names(evs))
	h := evs[0].payload["pull_request"].(map[string]any)["head"].(map[string]any)
	assert.Equal(t, "bob:fix", h["label"])
	assert.Equal(t, "bob/app", h["repo"].(map[string]any)["full_name"])
	_, list := srv.get(t, pullsAPI("alice/app"), "")
	assert.Equal(t, "bob/app", list["pull_requests"].([]any)[0].(map[string]any)["source_repository"])

	t.Run("pushes_to_the_fork_update_it", func(t *testing.T) {
		next := pushOnto(t, forkURL, bob.git, "fix", "More", map[string]string{"more.txt": "m\n"})
		srv.deliver(t)
		evs := rcv.takeEvents()
		require.Equal(t, []string{"pull_request.synchronize"}, names(evs))
		assert.Equal(t, next, evs[0].payload["after"])
		_, v := srv.get(t, pullsAPI("alice/app", n), "")
		assert.Equal(t, next, v["head_commit"])
		_, c := srv.get(t, pullsAPI("alice/app", n, "compare"), "")
		assert.Len(t, c["commits"], 2)
		head = next
	})

	t.Run("maintainer_edits", func(t *testing.T) {
		got := pushOnto(t, forkURL, dave.git, "fix", "By a maintainer", map[string]string{"dave.txt": "d\n"})
		_, v := srv.get(t, pullsAPI("alice/app", n), "")
		assert.Equal(t, got, v["head_commit"], "the target's developers push to the source branch")
		head = got

		pushFiles(t, forkURL, bob.git, "other", "o", nil)
		_, err := pushOntoErr(t, forkURL, dave.git, "other", "nope")
		require.Error(t, err, "but no other branch")
		assert.Contains(t, err.Error(), "you may only push to the source branches of pull requests that allow edits by maintainers")
		_, err = pushOntoErr(t, forkURL, carol.git, "fix", "nope")
		assert.Error(t, err, "nor people without write access to the target")

		status, _ := srv.request(t, http.MethodPatch, pullsAPI("alice/app", n), alice.session, map[string]any{"allow_maintainer_edits": false})
		assert.Equal(t, http.StatusForbidden, status, "the author decides")
		status, body := srv.request(t, http.MethodPatch, pullsAPI("alice/app", n), bob.session, map[string]any{"allow_maintainer_edits": false})
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, err = pushOntoErr(t, forkURL, dave.git, "fix", "nope")
		assert.Error(t, err, "no longer allowed")
		status, _ = srv.request(t, http.MethodPatch, pullsAPI("alice/app", n), bob.session, map[string]any{"allow_maintainer_edits": true})
		require.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		rcv.take()
	})

	t.Run("merged_by_the_target", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("alice/app", n, "merge"), bob.session, map[string]any{"method": "merge"})
		assert.Equal(t, http.StatusForbidden, status, "the fork's author doesn't merge: %v", body)
		status, body = srv.post(t, pullsAPI("alice/app", n, "merge"), alice.session, map[string]any{"method": "merge", "delete_branch": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		files := gitFiles(t, srv.url+"/alice/app.git", alice, "main")
		assert.Equal(t, "v1 fixed\n", files["app.txt"])
		assert.Equal(t, "d\n", files["dave.txt"])
		assert.NotEmpty(t, srv.refs(t, bob, "bob/app")["branches/fix"], "the fork's branch is not deleted")
		_, err := pushOntoErr(t, forkURL, dave.git, "fix", "nope")
		assert.Error(t, err, "maintainer edits end with the pull request")
	})

	t.Run("survives_the_fork", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, repoAPI("bob/app")+"?confirm=app", bob.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, v := srv.get(t, pullsAPI("alice/app", n), "")
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "deleted fork", v["source_repository"])
		_, c := srv.get(t, pullsAPI("alice/app", n, "compare"), "")
		assert.NotEmpty(t, c["commits"], "still readable: the commits were copied")
	})
}

func TestOpenPullRequestFromDeletedFork(t *testing.T) {
	srv := startTestServer(t)
	alice, bob := srv.newUser(t, "alice"), srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "App", false)
	pushFiles(t, srv.url+"/alice/app.git", alice.git, "main", "v1", map[string]string{"app.txt": "v1\n"})
	srv.fork(t, bob, "alice/app", map[string]any{})
	pushFiles(t, srv.url+"/bob/app.git", bob.git, "fix", "Fix", map[string]string{"app.txt": "v2\n"})
	status, pr := srv.post(t, pullsAPI("alice/app"), bob.session, map[string]any{"title": "Fix", "source": "fix", "source_repository": "bob/app"})
	require.Equal(t, http.StatusOK, status, "%v", pr)
	status, _ = srv.request(t, http.MethodDelete, repoAPI("bob/app")+"?confirm=app", bob.session, nil)
	require.Equal(t, http.StatusOK, status)

	_, v := srv.get(t, pullsAPI("alice/app", pr["number"]), alice.session)
	assert.Equal(t, "deleted fork", v["source_repository"])
	assert.Equal(t, false, v["branches_exist"])
	assert.Equal(t, false, v["can_merge"])
	status, body := srv.post(t, pullsAPI("alice/app", pr["number"], "merge"), alice.session, map[string]any{"method": "merge"})
	assert.NotEqual(t, http.StatusOK, status, "%v", body)
}

func TestMaintainerEditsOverSSH(t *testing.T) {
	srv := startTestServer(t)
	alice, bob, dave := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "dave")
	srv.createRepo(t, alice.session, "App", true)
	for name, role := range map[string]string{"bob": "reporter", "dave": "developer"} {
		status, _ := srv.post(t, repoAPI("alice/app")+"/members", alice.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status)
	}
	pushFiles(t, srv.url+"/alice/app.git", alice.git, "main", "v1", map[string]string{"app.txt": "v1\n"})
	srv.fork(t, bob, "alice/app", map[string]any{})
	pushFiles(t, srv.url+"/bob/app.git", bob.git, "fix", "Fix", map[string]string{"app.txt": "v2\n"})
	status, pr := srv.post(t, pullsAPI("alice/app"), bob.session, map[string]any{"title": "Fix", "source": "fix", "source_repository": "bob/app"})
	require.Equal(t, http.StatusOK, status, "%v", pr)

	port := srv.startSSH(t)
	key := newSSHKey(t, "dave@x")
	status, body := srv.post(t, "/api/v1/user/ssh-keys", dave.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)
	dir := t.TempDir()
	g.mustRun(dir, "clone", g.url("bob/app"), ".") // a private fork, readable through the pull request
	g.mustRun(dir, "checkout", "fix")
	commitFile(t, g, dir, "dave.txt", "d")
	g.mustRun(dir, "push", "origin", "fix")
	out, err := g.run(dir, "push", "origin", "fix:other")
	require.Error(t, err)
	assert.True(t, strings.Contains(out, "you may only push to the source branches"), out)

	_, v := srv.get(t, pullsAPI("alice/app", pr["number"]), alice.session)
	assert.Equal(t, strings.TrimSpace(g.mustRun(dir, "rev-parse", "HEAD")), v["head_commit"], "the pull request follows")
}
