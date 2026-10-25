package server

import (
	"bytes"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *testServer) protectTags(t *testing.T, as user, repo string, rule map[string]any) map[string]any {
	t.Helper()
	status, body := s.post(t, repoAPI(repo)+"/protected-tags", as.session, rule)
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

// pushTagRef pushes refspec (e.g. "refs/tags/v1:refs/tags/v1", or
// ":refs/tags/v1" to delete) from a clone where tag points to a new commit
// on main when move is set, else to main's tip.
func pushTagRef(t *testing.T, url string, auth *githttp.BasicAuth, tag string, move bool, refspec string) error {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: auth})
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	target := head.Hash()
	if move {
		wt, err := repo.Worktree()
		require.NoError(t, err)
		target, err = wt.Commit("moved", &git.CommitOptions{AllowEmptyCommits: true, Author: &object.Signature{Name: "X", Email: "x@example.com", When: time.Now()}})
		require.NoError(t, err)
	}
	if tag != "" {
		require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName(tag), target)))
	}
	return repo.Push(&git.PushOptions{Auth: auth, Force: move, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(refspec)}})
}

// logBuffer collects log lines written by the server's goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func createTag(t *testing.T, url string, auth *githttp.BasicAuth, tag string) error {
	return pushTagRef(t, url, auth, tag, false, "refs/tags/"+tag+":refs/tags/"+tag)
}

func TestProtectedTags(t *testing.T) {
	t.Run("local", func(t *testing.T) { testProtectedTags(t) })
	t.Run("s3_fake", func(t *testing.T) { testProtectedTags(t, withS3(s3test.StartFake(t))) })
}

func testProtectedTags(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, opts...)
	srv := f.srv
	rules := repoAPI("owner/app") + "/protected-tags"

	t.Run("rules", func(t *testing.T) {
		status, _ := srv.post(t, rules, f.maint.session, map[string]any{"pattern": "v*"})
		assert.Equal(t, http.StatusForbidden, status, "owners manage the rules")
		status, body := srv.post(t, rules, f.owner.session, map[string]any{"pattern": "bad pattern"})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		status, body = srv.post(t, rules, f.owner.session, map[string]any{"pattern": "x", "allowed_users": []string{"nobody"}})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)

		rule := srv.protectTags(t, f.owner, "owner/app", map[string]any{"pattern": "refs/tags/v*"})
		assert.Equal(t, "v*", rule["pattern"])
		status, _ = srv.post(t, rules, f.owner.session, map[string]any{"pattern": "v*"})
		assert.Equal(t, http.StatusConflict, status)
		status, body = srv.get(t, rules, f.dev.session)
		require.Equal(t, http.StatusOK, status, "developers read them")
		assert.Len(t, body["rules"], 1)
		status, _ = srv.get(t, rules, f.reporter.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, rules, f.outer.session)
		assert.Equal(t, http.StatusNotFound, status)

		status, body = srv.request(t, http.MethodPatch, rules+"/"+rule["id"].(string), f.owner.session, map[string]any{"allowed_users": []string{"reporter"}})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, []any{"reporter"}, body["allowed_users"])
		assert.Equal(t, "v*", body["pattern"], "kept")
		status, _ = srv.request(t, http.MethodPatch, rules+"/"+rule["id"].(string), f.owner.session, map[string]any{"allowed_users": []string{}})
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("creating", func(t *testing.T) {
		err := createTag(t, f.url, f.dev.git, "v1.0.0")
		require.Error(t, err, "developers can't create protected tags")
		assert.Contains(t, err.Error(), "tag v1.0.0 is protected: only maintainers")
		assert.NotContains(t, srv.refs(t, f.owner, "owner/app"), "tags/v1.0.0")
		assert.NoError(t, createTag(t, f.url, f.dev.git, "nightly-1"), "other tags are free")
		assert.NoError(t, createTag(t, f.url, f.maint.git, "v1.0.0"), "maintainers can")

		srv.protectTags(t, f.owner, "owner/app", map[string]any{"pattern": "release-*", "allowed_users": []string{"dev"}})
		assert.NoError(t, createTag(t, f.url, f.dev.git, "release-1"), "listed users can")
		status, body := srv.post(t, "/api/v1/group", f.owner.session, map[string]any{"name": "releasers"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, "/api/v1/group/releasers/members", f.owner.session, map[string]any{"username": "dev", "role": "member"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.protectTags(t, f.owner, "owner/app", map[string]any{"pattern": "rc-*", "allowed_groups": []string{"releasers"}})
		assert.NoError(t, createTag(t, f.url, f.dev.git, "rc-1"), "so do listed groups' members")

		ci := srv.repoToken(t, f.maint, "owner/app", "ci", "read:repository", "write:repository")
		err = createTag(t, f.url, &githttp.BasicAuth{Username: "ci", Password: ci["token"].(string)}, "v1.0.1")
		require.Error(t, err, "repository tokens never may")
		assert.Contains(t, err.Error(), "is protected")
	})

	t.Run("moving_and_deleting", func(t *testing.T) {
		err := pushTagRef(t, f.url, f.maint.git, "v1.0.0", true, "+refs/tags/v1.0.0:refs/tags/v1.0.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "protected tags can't be moved")
		err = pushTagRef(t, f.url, f.maint.git, "", false, ":refs/tags/v1.0.0")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "protected tags can't be deleted")
		assert.Contains(t, srv.refs(t, f.owner, "owner/app"), "tags/v1.0.0")

		assert.NoError(t, pushTagRef(t, f.url, f.dev.git, "nightly-1", true, "+refs/tags/nightly-1:refs/tags/nightly-1"), "unprotected tags move")

		logs := &logBuffer{}
		srv.deps.ProtectionApp.Logger = zerolog.New(logs)
		before := srv.refs(t, f.owner, "owner/app")["tags/v1.0.0"]
		require.NoError(t, pushTagRef(t, f.url, f.owner.git, "v1.0.0", true, "+refs/tags/v1.0.0:refs/tags/v1.0.0"), "owners may (logged)")
		assert.NotEqual(t, before, srv.refs(t, f.owner, "owner/app")["tags/v1.0.0"])
		require.NoError(t, pushTagRef(t, f.url, f.owner.git, "", false, ":refs/tags/v1.0.0"))
		assert.NotContains(t, srv.refs(t, f.owner, "owner/app"), "tags/v1.0.0")
		assert.Contains(t, logs.String(), `"action":"protected_tag.bypass"`)
		assert.Contains(t, logs.String(), `"rule":"tag v1.0.0 is protected: protected tags can't be deleted"`)
	})

	t.Run("releases", func(t *testing.T) {
		status, body := srv.post(t, releasesAPI("owner/app"), f.dev.session, map[string]any{"tag_name": "v2.0.0"})
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		assert.Equal(t, "TAG_PROTECTED", body["type"])
		assert.NotContains(t, srv.refs(t, f.owner, "owner/app"), "tags/v2.0.0")

		draft := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v2.0.0", "draft": true})
		status, body = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draft["id"].(string)), f.dev.session, map[string]any{"draft": false})
		assert.Equal(t, http.StatusForbidden, status, "publishing creates the tag: %v", body)
		status, body = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draft["id"].(string)), f.maint.session, map[string]any{"draft": false})
		assert.Equal(t, http.StatusOK, status, "%v", body)

		ci := srv.repoToken(t, f.maint, "owner/app", "ci2", "read:repository", "write:repository")
		status, body, _ = srv.do(t, http.MethodPost, releasesAPI("owner/app"), tokenHeader(ci["token"].(string)), map[string]any{"tag_name": "v3.0.0"})
		assert.Equal(t, http.StatusForbidden, status, "%v", body)

		require.NoError(t, createTag(t, f.url, f.maint.git, "v4.0.0"))
		status, body, _ = srv.do(t, http.MethodPost, releasesAPI("owner/app"), tokenHeader(ci["token"].(string)), map[string]any{"tag_name": "v4.0.0"})
		assert.Equal(t, http.StatusOK, status, "an existing protected tag needs no rights: %v", body)
		status, body = srv.post(t, releasesAPI("owner/app"), f.dev.session, map[string]any{"tag_name": "release-2"})
		assert.Equal(t, http.StatusOK, status, "listed users: %v", body)
	})
}

func TestProtectedTagsOverSSH(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	bob := srv.newUser(t, "bob")
	status, body := srv.post(t, repoAPI("alice/site")+"/members", alice.session, map[string]any{"username": "bob", "role": "maintainer"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.protectTags(t, alice, "alice/site", map[string]any{"pattern": "v*"})
	port := srv.startSSH(t)
	key := newSSHKey(t, "bob@x")
	status, body = srv.post(t, "/api/v1/user/ssh-keys", bob.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)

	dir := t.TempDir()
	g.mustRun(dir, "clone", g.url("alice/site"), ".")
	g.mustRun(dir, "tag", "v1")
	g.mustRun(dir, "push", "origin", "v1")
	out, err := g.run(dir, "push", "origin", "--delete", "v1")
	require.Error(t, err)
	assert.Contains(t, out, "tag v1 is protected: protected tags can't be deleted")
}
