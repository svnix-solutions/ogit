package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPullRequestLineMerge(t *testing.T) {
	t.Run("local", func(t *testing.T) { testPullRequestLineMerge(t) })
	t.Run("s3_fake", func(t *testing.T) { testPullRequestLineMerge(t, withS3(s3test.StartFake(t))) })
}

func testPullRequestLineMerge(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, opts...)
	srv := f.srv
	base := "one\ntwo\nthree\nfour\nfive\nsix\n"
	pushFiles(t, f.url, f.owner.git, "main", "base", map[string]string{"code.txt": base})

	t.Run("different lines merge", func(t *testing.T) {
		for _, method := range []string{"merge", "squash", "rebase"} {
			file := method + ".txt"
			pushFiles(t, f.url, f.owner.git, "main", "base", map[string]string{file: base})
			branch := "lines-" + method
			pushFiles(t, f.url, f.dev.git, branch, "topic", map[string]string{file: strings.Replace(base, "six", "changed", 1)})
			_, pr := f.open(t, f.dev, branch, nil, nil)
			pushFiles(t, f.url, f.owner.git, "main", "main moves", map[string]string{file: strings.Replace(base, "one", "changed", 1)})

			status, body := srv.get(t, pullsAPI("owner/app", pr["number"]), f.maint.session)
			require.Equal(t, http.StatusOK, status)
			assert.Equal(t, true, body["mergeable"], method)
			assert.Empty(t, body["conflicts"], method)
			status, body = srv.get(t, pullsAPI("owner/app", pr["number"], "conflicts"), f.maint.session)
			require.Equal(t, http.StatusOK, status)
			assert.Empty(t, body["files"], method)

			status, body = srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": method})
			require.Equal(t, http.StatusOK, status, "%v", body)
			assert.Equal(t, "changed\ntwo\nthree\nfour\nfive\nchanged\n", gitFiles(t, f.url, f.owner, "main")[file], method)
		}
	})

	t.Run("the same line conflicts", func(t *testing.T) {
		before := gitFiles(t, f.url, f.owner, "main")["code.txt"]
		pushFiles(t, f.url, f.dev.git, "clash", "topic", map[string]string{"code.txt": strings.Replace(before, "three\n", "from topic\n", 1)})
		_, pr := f.open(t, f.dev, "clash", nil, nil)
		pushFiles(t, f.url, f.owner.git, "main", "main moves", map[string]string{"code.txt": strings.Replace(before, "three\n", "from main\n", 1)})

		status, body := srv.get(t, pullsAPI("owner/app", pr["number"]), f.maint.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["mergeable"])
		assert.Equal(t, []any{"code.txt"}, body["conflicts"])

		status, body = srv.get(t, pullsAPI("owner/app", pr["number"], "conflicts"), f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, []any{map[string]any{
			"path": "code.txt", "reason": "content", "truncated": false,
			"hunks": []any{map[string]any{"target_line": float64(3), "source_line": float64(3), "target": "from main\n", "source": "from topic\n"}},
		}}, body["files"])

		status, _ = srv.get(t, pullsAPI("owner/app", pr["number"], "conflicts"), f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "private repositories stay hidden")
		status, _ = srv.get(t, pullsAPI("owner/app", pr["number"], "conflicts"), "")
		assert.Equal(t, http.StatusNotFound, status)

		status, body = srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "MERGE_CONFLICT", body["type"])

		status, body = srv.post(t, pullsAPI("owner/app", pr["number"], "close"), f.maint.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.get(t, pullsAPI("owner/app", pr["number"], "conflicts"), f.maint.session)
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, body["files"], "closed pull requests have no conflicts")
	})
}
