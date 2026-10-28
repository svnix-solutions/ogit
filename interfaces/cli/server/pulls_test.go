package server

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pullsAPI(repo string, rest ...any) string {
	s := repoAPI(repo) + "/pulls"
	for _, r := range rest {
		s += fmt.Sprintf("/%v", r)
	}
	return s
}

// gitFiles returns the files at the tip of a branch.
func gitFiles(t *testing.T, url string, as user, branch string) map[string]string {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: as.git, ReferenceName: plumbing.NewBranchReferenceName(branch), SingleBranch: true})
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	files := map[string]string{}
	require.NoError(t, tree.Files().ForEach(func(f *object.File) error {
		files[f.Name], err = f.Contents()
		return err
	}))
	return files
}

type pullFixture struct {
	srv                                *testServer
	owner, maint, dev, reporter, outer user
	url                                string
}

func newPullFixture(t *testing.T, private bool, opts ...func(*config.Config)) *pullFixture {
	srv := startTestServer(t, opts...)
	f := &pullFixture{srv: srv, owner: srv.newUser(t, "owner"), maint: srv.newUser(t, "maint"), dev: srv.newUser(t, "dev"),
		reporter: srv.newUser(t, "reporter"), outer: srv.newUser(t, "outer")}
	srv.createRepo(t, f.owner.session, "App", private)
	for name, role := range map[string]string{"maint": "maintainer", "dev": "developer", "reporter": "reporter"} {
		status, body := srv.post(t, repoAPI("owner/app")+"/members", f.owner.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	f.url = srv.url + "/owner/app.git"
	pushFiles(t, f.url, f.owner.git, "main", "shared base", map[string]string{"shared.txt": "base\n"})
	return f
}

func (f *pullFixture) open(t *testing.T, as user, branch string, files map[string]string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	if files != nil {
		pushFiles(t, f.url, f.dev.git, branch, "work on "+branch, files)
	}
	body := map[string]any{"title": "PR from " + branch, "source": branch}
	for k, v := range extra {
		body[k] = v
	}
	return f.srv.post(t, pullsAPI("owner/app"), as.session, body)
}

func TestPullRequestLifecycle(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPullRequestLifecycle(t) })
	t.Run("s3_fake", func(t *testing.T) { testPullRequestLifecycle(t, withS3(s3test.StartFake(t))) })
}

func testPullRequestLifecycle(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, opts...)
	srv := f.srv

	t.Run("who_opens", func(t *testing.T) {
		pushFiles(t, f.url, f.dev.git, "feature", "feature work", map[string]string{"a.txt": "a\n"})
		status, _ := f.open(t, f.reporter, "feature", nil, nil)
		assert.Equal(t, http.StatusForbidden, status, "reporters don't open")
		status, _ = f.open(t, f.outer, "feature", nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "outsiders don't see the repository")

		status, body := f.open(t, f.dev, "feature", nil, map[string]any{"body": "Adds a.txt"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.EqualValues(t, 1, body["number"])
		assert.Equal(t, "main", body["target_branch"], "the default branch")
		assert.Equal(t, "dev", body["author"])
		assert.Equal(t, true, body["mergeable"])
		assert.Equal(t, false, body["can_merge"], "developers don't merge")
		assert.Equal(t, true, body["can_edit"], "the author edits")

		for _, tc := range []struct {
			body map[string]any
			want string
		}{
			{map[string]any{"title": "again", "source": "feature"}, "PULL_REQUEST_EXISTS"},
			{map[string]any{"title": "x", "source": "nope"}, "BRANCH_NOT_FOUND"},
			{map[string]any{"title": "x", "source": "main"}, "SAME_BRANCH"},
			{map[string]any{"title": "  ", "source": "feature", "target": "other"}, "TITLE_REQUIRED"},
		} {
			_, body := srv.post(t, pullsAPI("owner/app"), f.dev.session, tc.body)
			assert.Equal(t, tc.want, body["type"], "%v", tc.body)
		}
		pushFiles(t, f.url, f.dev.git, "behind", "nothing new", map[string]string{})
		_, body = srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "x", "source": "main", "target": "behind"})
		assert.Equal(t, "NO_CHANGES", body["type"])
	})

	t.Run("reading", func(t *testing.T) {
		status, body := srv.get(t, pullsAPI("owner/app"), f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["pull_requests"], 1)
		assert.EqualValues(t, 1, body["counts"].(map[string]any)["open"])
		status, body = srv.get(t, pullsAPI("owner/app", 1), f.maint.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, body["can_merge"])
		assert.Equal(t, true, body["can_edit"])
		status, body = srv.get(t, pullsAPI("owner/app", 1), f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["can_edit"])
		for _, as := range []string{"", f.outer.session} {
			status, _ = srv.get(t, pullsAPI("owner/app", 1), as)
			assert.Equal(t, http.StatusNotFound, status, "private repository")
		}
		status, body = srv.get(t, pullsAPI("owner/app", 1, "compare"), f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["commits"], 1)
		assert.Equal(t, "a.txt", body["diff"].(map[string]any)["files"].([]any)[0].(map[string]any)["path"])
		status, _ = srv.get(t, pullsAPI("owner/app", 99), f.owner.session)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("editing_and_drafts", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, pullsAPI("owner/app", 1), f.reporter.session, map[string]any{"title": "hijack"})
		assert.Equal(t, http.StatusForbidden, status)
		status, body := srv.request(t, http.MethodPatch, pullsAPI("owner/app", 1), f.dev.session, map[string]any{"title": "Add a.txt", "draft": true})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Add a.txt", body["title"])
		assert.Equal(t, false, body["can_merge"], "a draft")
		status, body = srv.post(t, pullsAPI("owner/app", 1, "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "DRAFT", body["type"])
		status, _ = srv.request(t, http.MethodPatch, pullsAPI("owner/app", 1), f.dev.session, map[string]any{"draft": false})
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("conflicts", func(t *testing.T) {
		_, body := f.open(t, f.dev, "clash", map[string]string{"shared.txt": "from clash\n"}, nil)
		number := body["number"]
		pushFiles(t, f.url, f.owner.git, "main", "main moves", map[string]string{"shared.txt": "from main\n"})
		status, body := srv.get(t, pullsAPI("owner/app", number), f.maint.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["mergeable"])
		assert.Equal(t, []any{"shared.txt"}, body["conflicts"])
		status, body = srv.post(t, pullsAPI("owner/app", number, "merge"), f.maint.session, map[string]any{"method": "squash"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "MERGE_CONFLICT", body["type"])
		assert.Contains(t, body["details"], "shared.txt")
	})

	t.Run("merge_methods", func(t *testing.T) {
		status, body := srv.post(t, pullsAPI("owner/app", 1, "merge"), f.dev.session, map[string]any{"method": "merge"})
		assert.Equal(t, http.StatusForbidden, status, "developers don't merge")
		status, body = srv.post(t, pullsAPI("owner/app", 1, "merge"), f.maint.session, map[string]any{"method": "fast"})
		assert.Equal(t, "INVALID_MERGE_METHOD", body["type"])

		status, body = srv.post(t, pullsAPI("owner/app", 1, "merge"), f.maint.session, map[string]any{"method": "merge"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "merged", body["state"])
		assert.Equal(t, "maint", body["merged_by"])
		assert.Equal(t, "merge", body["merge_method"])

		_, b := f.open(t, f.dev, "squashme", map[string]string{"b.txt": "b\n"}, nil)
		status, body = srv.post(t, pullsAPI("owner/app", b["number"], "merge"), f.owner.session, map[string]any{"method": "squash"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, c := f.open(t, f.dev, "rebaseme", map[string]string{"c.txt": "c\n"}, nil)
		status, body = srv.post(t, pullsAPI("owner/app", c["number"], "merge"), f.maint.session, map[string]any{"method": "rebase", "delete_branch": true})
		require.Equal(t, http.StatusOK, status, "%v", body)

		files := gitFiles(t, f.url, f.owner, "main")
		assert.Equal(t, "a\n", files["a.txt"], "merged")
		assert.Equal(t, "b\n", files["b.txt"], "squashed")
		assert.Equal(t, "c\n", files["c.txt"], "rebased")
		assert.Equal(t, "from main\n", files["shared.txt"], "the conflicting pull request is still open")

		status, body = srv.get(t, repoAPI("owner/app")+"/refs", f.owner.session)
		require.Equal(t, http.StatusOK, status)
		var branches []string
		for _, b := range body["branches"].([]any) {
			branches = append(branches, b.(map[string]any)["name"].(string))
		}
		assert.NotContains(t, branches, "rebaseme", "deleted after merge")
		assert.Contains(t, branches, "squashme", "kept when not asked")

		status, body = srv.get(t, pullsAPI("owner/app", 1, "compare"), f.reporter.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["commits"], 1, "a merged pull request keeps its commits")
		status, body = srv.post(t, pullsAPI("owner/app", 1, "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, "NOT_OPEN", body["type"])
	})

	t.Run("close_and_reopen", func(t *testing.T) {
		_, body := f.open(t, f.dev, "maybe", map[string]string{"maybe.txt": "?\n"}, nil)
		n := body["number"]
		status, _ := srv.post(t, pullsAPI("owner/app", n, "close"), f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, body = srv.post(t, pullsAPI("owner/app", n, "close"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "closed", body["state"])
		status, body = srv.get(t, pullsAPI("owner/app")+"?state=closed", f.dev.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["pull_requests"], 1)
		status, body = srv.post(t, pullsAPI("owner/app", n, "reopen"), f.maint.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "open", body["state"])
	})

	t.Run("archived", func(t *testing.T) {
		_, body := f.open(t, f.dev, "late", map[string]string{"late.txt": "late\n"}, nil)
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": true})
		require.Equal(t, http.StatusOK, status)
		status, b := srv.post(t, pullsAPI("owner/app", body["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, "REPOSITORY_ARCHIVED", b["type"], "status %d", status)
		_, b = srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "x", "source": "maybe"})
		assert.Equal(t, "REPOSITORY_ARCHIVED", b["type"])
	})
}

func TestPublicPullRequests(t *testing.T) {
	f := newPullFixture(t, false)
	status, _ := f.open(t, f.dev, "feature", map[string]string{"a.txt": "a\n"}, nil)
	require.Equal(t, http.StatusOK, status)
	for _, path := range []string{pullsAPI("owner/app"), pullsAPI("owner/app", 1), pullsAPI("owner/app", 1, "compare")} {
		status, body := f.srv.get(t, path, "")
		assert.Equal(t, http.StatusOK, status, "%s: %v", path, body)
	}
	status, body := f.srv.get(t, pullsAPI("owner/app", 1), "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, false, body["can_edit"])
	assert.Equal(t, false, body["can_merge"])
	status, _ = f.srv.post(t, pullsAPI("owner/app", 1, "close"), "", nil)
	assert.NotEqual(t, http.StatusOK, status)
}

// Two pull requests merged at the same time: each succeeds or is told the
// target moved; none is reported merged without being in main.
func TestConcurrentMerges(t *testing.T) {
	f := newPullFixture(t, true)
	var numbers []any
	for _, b := range []string{"one", "two", "three"} {
		_, body := f.open(t, f.dev, b, map[string]string{b + ".txt": b + "\n"}, nil)
		numbers = append(numbers, body["number"])
	}
	results := make([]map[string]any, len(numbers))
	var wg sync.WaitGroup
	for i, n := range numbers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = f.srv.post(t, pullsAPI("owner/app", n, "merge"), f.maint.session, map[string]any{"method": "merge"})
		}()
	}
	wg.Wait()

	files := gitFiles(t, f.url, f.owner, "main")
	merged := 0
	for i, b := range []string{"one", "two", "three"} {
		if results[i]["state"] == "merged" {
			merged++
			assert.Equal(t, b+"\n", files[b+".txt"], "%s was reported merged", b)
		} else {
			assert.Equal(t, "TARGET_MOVED", results[i]["type"], "%s: %v", b, results[i])
		}
	}
	assert.Positive(t, merged)
}
