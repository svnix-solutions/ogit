package server

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (s *testServer) protect(t *testing.T, as user, repo string, rule map[string]any) map[string]any {
	t.Helper()
	status, body := s.post(t, repoAPI(repo)+"/branch-protection", as.session, rule)
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

func (s *testServer) unprotect(t *testing.T, as user, repo string, rule map[string]any) {
	t.Helper()
	status, body := s.request(t, http.MethodDelete, repoAPI(repo)+"/branch-protection/"+rule["id"].(string), as.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)
}

// forcePush replaces branch with a new unrelated commit.
func forcePush(t *testing.T, url string, auth *githttp.BasicAuth, branch string) error {
	t.Helper()
	repo, err := git.PlainInit(t.TempDir(), false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	h, err := wt.Commit("rewritten", &git.CommitOptions{AllowEmptyCommits: true, Author: &object.Signature{Name: "X", Email: "x@example.com", When: time.Now()}})
	require.NoError(t, err)
	_, err = repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{url}})
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("tmp"), h)))
	return repo.Push(&git.PushOptions{Auth: auth, Force: true, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/tmp:refs/heads/" + branch)}})
}

// deleteRemoteBranch pushes a deletion of branch.
func deleteRemoteBranch(t *testing.T, url string, auth *githttp.BasicAuth, branch string) error {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: auth})
	require.NoError(t, err)
	return repo.Push(&git.PushOptions{Auth: auth, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec(":refs/heads/" + branch)}})
}

// tip returns a branch's commit, "" if it doesn't exist.
func tip(t *testing.T, s *testServer, as user, repo, branch string) string {
	t.Helper()
	_, body := s.get(t, repoAPI(repo)+"/refs", as.session)
	for _, b := range body["branches"].([]any) {
		b := b.(map[string]any)
		if b["name"] == branch {
			return b["commit"].(map[string]any)["hash"].(string)
		}
	}
	return ""
}

func TestBranchProtectionRules(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	rules := repoAPI("owner/app") + "/branch-protection"
	status, body := srv.post(t, "/api/v1/group", f.owner.session, map[string]any{"name": "releasers"})
	require.Equal(t, http.StatusOK, status, "%v", body)

	t.Run("owners_manage_developers_read", func(t *testing.T) {
		status, _ := srv.post(t, rules, f.maint.session, map[string]any{"pattern": "main"})
		assert.Equal(t, http.StatusForbidden, status, "maintainers can't change protection")
		for _, as := range []user{f.owner, f.maint, f.dev} {
			status, _ := srv.get(t, rules, as.session)
			assert.Equal(t, http.StatusOK, status, as.name)
		}
		status, _ = srv.get(t, rules, f.reporter.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.get(t, rules, f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "a private repository stays hidden")
	})

	t.Run("validation", func(t *testing.T) {
		for _, rule := range []map[string]any{
			{},
			{"pattern": "a b"},
			{"pattern": "/main"},
			{"pattern": "main", "required_approvals": 11},
			{"pattern": "main", "required_approvals": -2},
		} {
			status, body := srv.post(t, rules, f.owner.session, rule)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", rule, body)
			assert.Equal(t, "INVALID_RULE", body["type"], "%v", rule)
		}
		status, body := srv.post(t, rules, f.owner.session, map[string]any{"pattern": "main", "allowed_users": []string{"nobody"}})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "UNKNOWN_USER", body["type"])
		status, body = srv.post(t, rules, f.owner.session, map[string]any{"pattern": "main", "allowed_groups": []string{"nope"}})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "UNKNOWN_GROUP", body["type"])
	})

	t.Run("crud", func(t *testing.T) {
		r := srv.protect(t, f.owner, "owner/app", map[string]any{
			"pattern": "refs/heads/release/*", "require_pull_request": true, "required_approvals": 2,
			"allowed_users": []string{"maint", "maint"}, "allowed_groups": []string{"releasers"},
		})
		assert.Equal(t, "release/*", r["pattern"], "refs/heads/ is dropped")
		assert.Equal(t, false, r["allow_force_push"])
		assert.Equal(t, false, r["allow_deletion"])
		assert.Equal(t, true, r["require_pull_request"])
		assert.EqualValues(t, 2, r["required_approvals"])
		assert.Equal(t, []any{"maint"}, r["allowed_users"], "once")
		assert.Equal(t, []any{"releasers"}, r["allowed_groups"])

		status, body := srv.post(t, rules, f.owner.session, map[string]any{"pattern": "release/*"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "RULE_EXISTS", body["type"])

		id := r["id"].(string)
		status, body = srv.request(t, http.MethodPatch, rules+"/"+id, f.owner.session, map[string]any{"required_approvals": -1, "allowed_users": []string{}})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Nil(t, body["required_approvals"], "back to the repository's")
		assert.Equal(t, []any{}, body["allowed_users"])
		assert.Equal(t, []any{"releasers"}, body["allowed_groups"], "kept")
		assert.Equal(t, true, body["require_pull_request"], "kept")

		status, _ = srv.request(t, http.MethodPatch, rules+"/"+id, f.maint.session, map[string]any{"allow_deletion": true})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, rules+"/"+id, f.maint.session, nil)
		assert.Equal(t, http.StatusForbidden, status)

		srv.createRepo(t, f.owner.session, "Other", true)
		status, _ = srv.get(t, repoAPI("owner/other")+"/branch-protection/"+id, f.owner.session)
		assert.Equal(t, http.StatusNotFound, status, "rules belong to their repository")

		srv.unprotect(t, f.owner, "owner/app", r)
		status, _ = srv.get(t, rules+"/"+id, f.owner.session)
		assert.Equal(t, http.StatusNotFound, status)
	})
}

func TestBranchProtectionPushes(t *testing.T) {
	t.Run("local", func(t *testing.T) { testBranchProtectionPushes(t) })
	t.Run("s3_fake", func(t *testing.T) { testBranchProtectionPushes(t, withS3(s3test.StartFake(t))) })
}

func testBranchProtectionPushes(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, opts...)
	srv := f.srv
	rule := srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "main"})

	t.Run("fast_forwards_pass", func(t *testing.T) {
		pushOnto(t, f.url, f.dev.git, "main", "normal change", map[string]string{"a.txt": "a\n"})
	})

	t.Run("force_pushes_refused", func(t *testing.T) {
		before := tip(t, srv, f.owner, "owner/app", "main")
		err := forcePush(t, f.url, f.owner.git, "main")
		require.Error(t, err, "even for owners")
		assert.Contains(t, err.Error(), "branch main is protected: force-pushes are not allowed")
		assert.Equal(t, before, tip(t, srv, f.owner, "owner/app", "main"))

		pushFiles(t, f.url, f.dev.git, "scratch", "x", nil)
		assert.NoError(t, forcePush(t, f.url, f.dev.git, "scratch"), "unprotected branches")
	})

	t.Run("deletion_refused", func(t *testing.T) {
		err := deleteRemoteBranch(t, f.url, f.owner.git, "main")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deleting it is not allowed")
		assert.NotEmpty(t, tip(t, srv, f.owner, "owner/app", "main"))
	})

	t.Run("allowed_by_the_rule", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app")+"/branch-protection/"+rule["id"].(string), f.owner.session,
			map[string]any{"allow_force_push": true})
		require.Equal(t, http.StatusOK, status)
		assert.NoError(t, forcePush(t, f.url, f.dev.git, "main"))
	})

	t.Run("one_ref_refused_others_applied", func(t *testing.T) {
		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "keep"})
		pushFiles(t, f.url, f.dev.git, "keep", "k", nil)
		pushFiles(t, f.url, f.dev.git, "free", "f", nil)
		repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: f.url, Auth: f.dev.git})
		require.NoError(t, err)
		err = repo.Push(&git.PushOptions{Auth: f.dev.git, RefSpecs: []gitconfig.RefSpec{":refs/heads/keep", ":refs/heads/free"}})
		require.Error(t, err)
		assert.NotEmpty(t, tip(t, srv, f.owner, "owner/app", "keep"), "protected")
		assert.Empty(t, tip(t, srv, f.owner, "owner/app", "free"), "the other deletion went through")
	})

	t.Run("pull_requests_only", func(t *testing.T) {
		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "release/**", "require_pull_request": true})
		pushFiles(t, f.url, f.dev.git, "release/v1", "cut v1", nil) // creating is allowed
		_, err := pushOntoErr(t, f.url, f.dev.git, "release/v1", "direct")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "changes must be merged through a pull request")

		pushFiles(t, f.url, f.dev.git, "fix", "fix", map[string]string{"fix.txt": "fixed\n"})
		status, body := srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "Fix", "source": "fix", "target": "release/v1"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, pullsAPI("owner/app", body["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
		require.Equal(t, http.StatusOK, status, "merging is the way in: %v", body)
	})

	t.Run("allowed_users_and_groups", func(t *testing.T) {
		// A group the owner is not in.
		status, body := srv.post(t, "/api/v1/group", f.maint.session, map[string]any{"name": "shippers"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.post(t, "/api/v1/group/shippers/members", f.maint.session, map[string]any{"username": "reporter", "role": "member"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "deploy", "allowed_users": []string{"maint"}, "allowed_groups": []string{"shippers"}})

		pushFiles(t, f.url, f.maint.git, "deploy", "create", nil) // listed users push
		_, err := pushOntoErr(t, f.url, f.dev.git, "deploy", "not me")
		require.Error(t, err, "developers not on the list")
		assert.Contains(t, err.Error(), "you are not allowed to push to it")
		_, err = pushOntoErr(t, f.url, f.owner.git, "deploy", "owner")
		require.Error(t, err, "owners too, without bypass")
		// The reporter is in the group but can't write the repository.
		_, err = pushOntoErr(t, f.url, f.reporter.git, "deploy", "reporter")
		require.Error(t, err)
	})

	t.Run("owners_bypass", func(t *testing.T) {
		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "prod", "require_pull_request": true, "owners_bypass": true})
		pushFiles(t, f.url, f.dev.git, "prod", "create", nil)
		_, err := pushOntoErr(t, f.url, f.dev.git, "prod", "dev")
		require.Error(t, err, "bypass is for owners only")
		_, err = pushOntoErr(t, f.url, f.maint.git, "prod", "maint")
		require.Error(t, err)
		_, err = pushOntoErr(t, f.url, f.owner.git, "prod", "owner")
		assert.NoError(t, err)
		assert.NoError(t, forcePush(t, f.url, f.owner.git, "prod"))
	})
}

// pushOntoErr is pushOnto returning the push error instead of failing.
func pushOntoErr(t *testing.T, url string, auth *githttp.BasicAuth, branch, msg string) (string, error) {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: auth, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true})
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)
	h, err := wt.Commit(msg, &git.CommitOptions{AllowEmptyCommits: true, Author: &object.Signature{Name: "P", Email: "p@example.com", When: time.Now()}})
	require.NoError(t, err)
	return h.String(), repo.Push(&git.PushOptions{Auth: auth})
}

func TestBranchProtectionMerges(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	rule := srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "main", "required_approvals": 1, "dismiss_stale_approvals": true})
	ruleURL := repoAPI("owner/app") + "/branch-protection/" + rule["id"].(string)

	status, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, nil)
	require.Equal(t, http.StatusOK, status, "%v", pr)
	n := pr["number"]
	get := func(as user) map[string]any {
		status, body := srv.get(t, pullsAPI("owner/app", n), as.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		return body
	}
	merge := func(as user) (int, map[string]any) {
		return srv.post(t, pullsAPI("owner/app", n, "merge"), as.session, map[string]any{"method": "merge", "delete_branch": true})
	}

	t.Run("rule_approvals_override_the_repository", func(t *testing.T) {
		v := get(f.maint)
		assert.EqualValues(t, 1, v["required_approvals"], "the repository needs none")
		assert.Equal(t, "main", v["protected_by"])
		assert.Equal(t, false, v["can_merge"])
		assert.Equal(t, "NEEDS_APPROVAL", v["merge_blocked"].(map[string]any)["type"])
		status, body := merge(f.maint)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "NEEDS_APPROVAL", body["type"])
	})

	t.Run("stale_approvals_are_dismissed", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", n, "reviews"), f.maint.session, map[string]any{"state": "approved"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, get(f.maint)["can_merge"])
		pushOnto(t, f.url, f.dev.git, "feature", "more", map[string]string{"f.txt": "2\n"})
		v := get(f.maint)
		assert.Equal(t, []any{}, v["approved"], "approved an older commit")
		assert.Equal(t, false, v["can_merge"])

		status, _ = srv.request(t, http.MethodPatch, ruleURL, f.owner.session, map[string]any{"dismiss_stale_approvals": false})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, []any{"maint"}, get(f.maint)["approved"], "kept without the option")
		status, _ = srv.request(t, http.MethodPatch, ruleURL, f.owner.session, map[string]any{"dismiss_stale_approvals": true})
		require.Equal(t, http.StatusOK, status)
		status, body = srv.post(t, pullsAPI("owner/app", n, "reviews"), f.maint.session, map[string]any{"state": "approved"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, get(f.maint)["can_merge"], "approved again on the new head")
	})

	t.Run("only_listed_mergers", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, ruleURL, f.owner.session, map[string]any{"allowed_users": []string{"owner"}})
		require.Equal(t, http.StatusOK, status)
		v := get(f.maint)
		assert.Equal(t, false, v["can_merge"])
		assert.Equal(t, "BRANCH_PROTECTED", v["merge_blocked"].(map[string]any)["type"])
		assert.Contains(t, v["merge_blocked"].(map[string]any)["message"], "you are not allowed to merge into it")
		status, body := merge(f.maint)
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		assert.Equal(t, "BRANCH_PROTECTED", body["type"])
		assert.Equal(t, true, get(f.owner)["can_merge"])
	})

	t.Run("owners_bypass_missing_approvals", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, ruleURL, f.owner.session, map[string]any{"required_approvals": 3})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, get(f.owner)["can_merge"], "no bypass yet")
		status, _ = srv.request(t, http.MethodPatch, ruleURL, f.owner.session, map[string]any{"owners_bypass": true})
		require.Equal(t, http.StatusOK, status)
		v := get(f.owner)
		assert.Equal(t, true, v["can_merge"], "%v", v["merge_blocked"])
		assert.Nil(t, v["merge_blocked"])

		srv.protect(t, f.owner, "owner/app", map[string]any{"pattern": "feature"})
		status, body := merge(f.owner)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "merged", body["state"])
		assert.NotEmpty(t, tip(t, srv, f.owner, "owner/app", "feature"), "a protected source branch is kept")
	})
}

func TestBranchProtectionOverSSH(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	srv.createRepo(t, alice.session, "Site", true)
	srv.protect(t, alice, "alice/site", map[string]any{"pattern": "main"})
	port := srv.startSSH(t)
	key := newSSHKey(t, "alice@x")
	status, body := srv.post(t, "/api/v1/user/ssh-keys", alice.session, map[string]any{"key": key.authorized})
	require.Equal(t, http.StatusOK, status, "%v", body)
	g := newGitCLI(t, key, port)

	dir := t.TempDir()
	g.mustRun(dir, "clone", g.url("alice/site"), ".")
	commitFile(t, g, dir, "x.txt", "1")
	g.mustRun(dir, "push", "origin", "HEAD:main")
	g.mustRun(dir, "commit", "--amend", "-m", "rewritten")
	out, err := g.run(dir, "push", "--force", "origin", "HEAD:main")
	require.Error(t, err)
	assert.Contains(t, out, "branch main is protected: force-pushes are not allowed")
	out, err = g.run(dir, "push", "origin", "--delete", "main")
	require.Error(t, err)
	assert.Contains(t, out, "deleting it is not allowed")

	// Deploy keys are never on a rule's list.
	srv.protect(t, alice, "alice/site", map[string]any{"pattern": "ci", "allowed_users": []string{"alice"}})
	deploy := newSSHKey(t, "deploy")
	status, body = srv.post(t, repoAPI("alice/site")+"/deploy-keys", alice.session, map[string]any{"name": "ci", "key": deploy.authorized, "read_only": false})
	require.Equal(t, http.StatusOK, status, "%v", body)
	d := newGitCLI(t, deploy, port)
	out, err = d.run(dir, "push", d.url("alice/site"), "HEAD:refs/heads/ci")
	require.Error(t, err)
	assert.Contains(t, out, "you are not allowed to push to it")
	_, err = d.run(dir, "push", d.url("alice/site"), "HEAD:refs/heads/other")
	assert.NoError(t, err, "deploy keys push to unprotected branches")
}
