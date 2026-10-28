package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cacheClient speaks GitHub's v1 cache API like @actions/cache.
type cacheClient struct {
	t     *testing.T
	srv   *testServer
	token string
}

func (c *cacheClient) do(method, path string, headers map[string]string, body []byte) (int, []byte, http.Header) {
	c.t.Helper()
	req, err := http.NewRequest(method, c.srv.url+"/_apis/artifactcache/"+path, bytes.NewReader(body))
	require.NoError(c.t, err)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json;api-version=6.0-preview.1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(c.t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header
}

func (c *cacheClient) lookup(version string, keys ...string) (int, map[string]any) {
	c.t.Helper()
	status, data, _ := c.do(http.MethodGet, "cache?keys="+url.QueryEscape(strings.Join(keys, ","))+"&version="+version, nil, nil)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return status, out
}

// save reserves, uploads content in chunks (the last first, in parallel)
// and commits.
func (c *cacheClient) save(key, version string, content []byte, chunk int) int {
	c.t.Helper()
	body, _ := json.Marshal(map[string]any{"key": key, "version": version, "cacheSize": len(content)})
	status, data, _ := c.do(http.MethodPost, "caches", map[string]string{"Content-Type": "application/json"}, body)
	if status != http.StatusCreated {
		return status
	}
	var r struct{ CacheId int64 }
	require.NoError(c.t, json.Unmarshal(data, &r))
	var wg sync.WaitGroup
	statuses := make(chan int, 64)
	for start := (len(content) - 1) / chunk * chunk; start >= 0; start -= chunk {
		end := min(start+chunk, len(content)) - 1
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			s, _, _ := c.do(http.MethodPatch, fmt.Sprintf("caches/%d", r.CacheId),
				map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes %d-%d/*", start, end)}, content[start:end+1])
			statuses <- s
		}(start, end)
	}
	wg.Wait()
	close(statuses)
	for s := range statuses {
		if s != http.StatusNoContent {
			return s
		}
	}
	body, _ = json.Marshal(map[string]any{"size": len(content)})
	status, _, _ = c.do(http.MethodPost, fmt.Sprintf("caches/%d", r.CacheId), map[string]string{"Content-Type": "application/json"}, body)
	return status
}

func (c *cacheClient) restore(version string, keys ...string) ([]byte, string) {
	c.t.Helper()
	status, hit := c.lookup(version, keys...)
	if status == http.StatusNoContent {
		return nil, ""
	}
	require.Equal(c.t, http.StatusOK, status, "%v", hit)
	s, body, _ := get(c.t, hit["archiveLocation"].(string), nil)
	require.Equal(c.t, http.StatusOK, s)
	return body, hit["cacheKey"].(string)
}

func TestActionsCache(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.Actions.CacheSizeLimit = 1 << 20 })
	srv := f.srv
	runner := f.runner(t)
	// runOn pushes to branch and claims the run.
	runOn := func(t *testing.T, branch string, files map[string]string) *application.Task {
		t.Helper()
		if branch == "main" {
			pushFiles(t, f.url, f.owner.git, "main", "work", files)
		} else {
			pushFiles(t, f.url, f.dev.git, branch, "work", files)
		}
		task := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, task)
		return task
	}
	var mainTask *application.Task
	var pending int64 // an upload reserved by the main run
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": workflow("  push:\n  pull_request:")})
	main := fetch0(t, runner, "ubuntu-latest")
	require.NotNil(t, main)
	onMain := &cacheClient{t: t, srv: srv, token: main.RuntimeToken}
	content := bytes.Repeat([]byte("node_modules "), 30000) // 390 KB

	t.Run("save_and_restore", func(t *testing.T) {
		status, _ := onMain.lookup("v1", "deps-linux-abc")
		assert.Equal(t, http.StatusNoContent, status, "a miss")
		require.Equal(t, http.StatusNoContent, onMain.save("Deps-Linux-abc", "v1", content, 100000))
		got, key := onMain.restore("v1", "deps-linux-abc")
		assert.Equal(t, "deps-linux-abc", key, "keys are case-insensitive")
		assert.True(t, bytes.Equal(content, got), "chunks assembled at their offsets")

		_, key = onMain.restore("v1", "deps-linux-zzz", "deps-linux-")
		assert.Equal(t, "deps-linux-abc", key, "a restore key matches by prefix")
		got, _ = onMain.restore("v2", "deps-linux-abc")
		assert.Nil(t, got, "another version is a miss")
		assert.Equal(t, http.StatusConflict, onMain.save("deps-linux-abc", "v1", []byte("x"), 1), "saved once")
	})

	t.Run("ranges", func(t *testing.T) {
		_, hit := onMain.lookup("v1", "deps-linux-abc")
		loc := hit["archiveLocation"].(string)
		req, _ := http.NewRequest(http.MethodHead, loc, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, fmt.Sprint(len(content)), resp.Header.Get("Content-Length"))
		assert.Equal(t, "bytes", resp.Header.Get("Accept-Ranges"))
		for rng, want := range map[string][]byte{"bytes=10-19": content[10:20], "bytes=-5": content[len(content)-5:], fmt.Sprintf("bytes=%d-", len(content)-3): content[len(content)-3:]} {
			status, body, header := get(t, loc, map[string]string{"Range": rng})
			assert.Equal(t, http.StatusPartialContent, status, rng)
			assert.Equal(t, want, body, rng)
			assert.NotEmpty(t, header.Get("Content-Range"))
		}
		status, _, _ := get(t, loc, map[string]string{"Range": fmt.Sprintf("bytes=%d-", len(content))})
		assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, status)
		status, _, _ = get(t, strings.Replace(loc, "sig=", "sig=x", 1), nil)
		assert.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("commit_checks", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"key": "gap", "version": "v1", "cacheSize": 20})
		_, data, _ := onMain.do(http.MethodPost, "caches", nil, body)
		var r struct{ CacheId int64 }
		require.NoError(t, json.Unmarshal(data, &r))
		s, _, _ := onMain.do(http.MethodPatch, fmt.Sprintf("caches/%d", r.CacheId), map[string]string{"Content-Range": "bytes 10-19/*"}, bytes.Repeat([]byte("y"), 10))
		require.Equal(t, http.StatusNoContent, s)
		body, _ = json.Marshal(map[string]any{"size": 20})
		s, _, _ = onMain.do(http.MethodPost, fmt.Sprintf("caches/%d", r.CacheId), nil, body)
		assert.Equal(t, http.StatusBadRequest, s, "bytes 0-9 are missing")
		s, _, _ = onMain.do(http.MethodPatch, fmt.Sprintf("caches/%d", r.CacheId), map[string]string{"Content-Range": "bytes 0-9/*"}, bytes.Repeat([]byte("x"), 10))
		require.Equal(t, http.StatusNoContent, s)
		body, _ = json.Marshal(map[string]any{"size": 25})
		s, _, _ = onMain.do(http.MethodPost, fmt.Sprintf("caches/%d", r.CacheId), nil, body)
		assert.Equal(t, http.StatusBadRequest, s, "the declared size")
		body, _ = json.Marshal(map[string]any{"size": 20})
		s, data, _ = onMain.do(http.MethodPost, fmt.Sprintf("caches/%d", r.CacheId), nil, body)
		assert.Equal(t, http.StatusNoContent, s, "%s", data)
		got, _ := onMain.restore("v1", "gap")
		assert.Equal(t, "xxxxxxxxxxyyyyyyyyyy", string(got))

		// A gap the right total size doesn't fill.
		body, _ = json.Marshal(map[string]any{"key": "holes", "version": "v1", "cacheSize": 20})
		_, data, _ = onMain.do(http.MethodPost, "caches", nil, body)
		require.NoError(t, json.Unmarshal(data, &r))
		for _, rng := range []string{"bytes 0-9/*", "bytes 15-24/*"} {
			s, _, _ := onMain.do(http.MethodPatch, fmt.Sprintf("caches/%d", r.CacheId), map[string]string{"Content-Range": rng}, bytes.Repeat([]byte("z"), 10))
			require.Equal(t, http.StatusNoContent, s)
		}
		body, _ = json.Marshal(map[string]any{"size": 20})
		s, _, _ = onMain.do(http.MethodPost, fmt.Sprintf("caches/%d", r.CacheId), nil, body)
		assert.Equal(t, http.StatusBadRequest, s, "bytes 10-14 are missing")

		body, _ = json.Marshal(map[string]any{"key": "huge", "version": "v1", "cacheSize": 1<<20 + 1})
		s, _, _ = onMain.do(http.MethodPost, "caches", nil, body)
		assert.Equal(t, http.StatusRequestEntityTooLarge, s, "refused when reserved")
		body, _ = json.Marshal(map[string]any{"key": "main-pending", "version": "v1", "cacheSize": 10})
		_, data, _ = onMain.do(http.MethodPost, "caches", nil, body)
		require.NoError(t, json.Unmarshal(data, &r))
		pending = r.CacheId
	})

	t.Run("scopes", func(t *testing.T) {
		finish(t, runner, main, "success")
		status, _ := onMain.lookup("v1", "deps-linux-abc")
		assert.Equal(t, http.StatusUnauthorized, status, "the runtime token ends with its run")

		feature := runOn(t, "feature", map[string]string{"f.txt": "f\n"})
		onFeature := &cacheClient{t: t, srv: srv, token: feature.RuntimeToken}
		s, _, _ := onFeature.do(http.MethodPatch, fmt.Sprintf("caches/%d", pending), map[string]string{"Content-Range": "bytes 0-9/*"}, bytes.Repeat([]byte("z"), 10))
		assert.Equal(t, http.StatusNotFound, s, "another ref's upload")
		_, key := onFeature.restore("v1", "deps-linux-abc")
		assert.Equal(t, "deps-linux-abc", key, "a branch restores the default branch's")
		require.Equal(t, http.StatusNoContent, onFeature.save("feature-only", "v1", []byte("feature"), 10))
		got, _ := onFeature.restore("v1", "feature-only")
		assert.Equal(t, "feature", string(got))
		finish(t, runner, feature, "success")

		status, pr := f.open(t, f.dev, "feature", nil, nil)
		require.Equal(t, http.StatusOK, status, "%v", pr)
		prRun := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, prRun)
		require.Equal(t, "pull_request", prRun.Event)
		onPR := &cacheClient{t: t, srv: srv, token: prRun.RuntimeToken}
		_, key = onPR.restore("v1", "deps-linux-abc")
		assert.Equal(t, "deps-linux-abc", key, "a pull request restores its base branch's")
		got, _ = onPR.restore("v1", "feature-only")
		assert.Nil(t, got, "not another branch's")

		finish(t, runner, prRun, "success")

		// A pull request into another branch restores that branch's.
		release := runOn(t, "release", map[string]string{"r.txt": "r\n"})
		require.Equal(t, http.StatusNoContent, (&cacheClient{t: t, srv: srv, token: release.RuntimeToken}).save("release-only", "v1", []byte("release"), 10))
		finish(t, runner, release, "success")
		pushFiles(t, f.url, f.dev.git, "fix", "fix", map[string]string{"x.txt": "x\n"})
		finish(t, runner, fetch0(t, runner, "ubuntu-latest"), "success") // the push to fix
		status, pr = f.srv.post(t, pullsAPI("owner/app"), f.dev.session, map[string]any{"title": "Fix", "source": "fix", "target": "release"})
		require.Equal(t, http.StatusOK, status, "%v", pr)
		prRun = fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, prRun)
		onPR = &cacheClient{t: t, srv: srv, token: prRun.RuntimeToken}
		got, _ = onPR.restore("v1", "release-only")
		assert.Equal(t, "release", string(got), "its base branch")

		require.NoError(t, srv.deps.Database.Db.Model(&domain.ActionRun{}).Where("id = ?", prRun.RunId).Update("from_fork", true).Error)
		assert.Equal(t, http.StatusForbidden, onPR.save("fork-key", "v1", []byte("x"), 1), "forks don't save")
		_, key = onPR.restore("v1", "deps-linux-abc")
		assert.Equal(t, "deps-linux-abc", key, "but they restore")
		finish(t, runner, prRun, "success")

		again := runOn(t, "main", map[string]string{"m.txt": "m\n"})
		got, _ = (&cacheClient{t: t, srv: srv, token: again.RuntimeToken}).restore("v1", "feature-only")
		assert.Nil(t, got, "the default branch doesn't see a branch's")
		mainTask = again
	})

	t.Run("limit_and_eviction", func(t *testing.T) {
		on := &cacheClient{t: t, srv: srv, token: mainTask.RuntimeToken}
		assert.Equal(t, http.StatusRequestEntityTooLarge, on.save("huge", "v1", bytes.Repeat([]byte("h"), 1<<20+1), 1<<20+1))
		require.Equal(t, http.StatusNoContent, on.save("big-1", "v1", bytes.Repeat([]byte("1"), 400000), 200000))
		_, _ = on.restore("v1", "deps-linux-abc") // used most recently
		require.Equal(t, http.StatusNoContent, on.save("big-2", "v1", bytes.Repeat([]byte("2"), 400000), 200000))
		got, _ := on.restore("v1", "big-1")
		assert.Nil(t, got, "the least recently used went over the limit")
		got, _ = on.restore("v1", "gap")
		assert.Equal(t, "xxxxxxxxxxyyyyyyyyyy", string(got), "only as much as needed: a small older entry that fits stays")
		_, key := on.restore("v1", "deps-linux-abc")
		assert.Equal(t, "deps-linux-abc", key)
	})

	t.Run("maintainers", func(t *testing.T) {
		status, _ := srv.get(t, actionsAPI("owner/app")+"/caches", f.dev.session)
		assert.Equal(t, http.StatusForbidden, status)
		status, body := srv.get(t, actionsAPI("owner/app")+"/caches", f.maint.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.EqualValues(t, 1<<20, body["limit"])
		var id string
		keys := []string{}
		for _, e := range body["caches"].([]any) {
			keys = append(keys, e.(map[string]any)["key"].(string))
			if e.(map[string]any)["key"] == "gap" {
				id = fmt.Sprint(int64(e.(map[string]any)["id"].(float64)))
			}
		}
		assert.Contains(t, keys, "deps-linux-abc")
		require.NotEmpty(t, id)
		status, _ = srv.request(t, http.MethodDelete, actionsAPI("owner/app")+"/caches/"+id, f.maint.session, nil)
		assert.Equal(t, http.StatusOK, status)
		got, _ := (&cacheClient{t: t, srv: srv, token: mainTask.RuntimeToken}).restore("v1", "gap")
		assert.Nil(t, got)
	})

	t.Run("sweep", func(t *testing.T) {
		var entry domain.ActionCache
		require.NoError(t, srv.deps.Database.Db.Where("cache_key = ?", "deps-linux-abc").First(&entry).Error)
		key := srv.repoStorageKey(t, "owner/app")
		rc, err := srv.deps.Storage.OpenFile(key, "actions-cache", entry.Id)
		require.NoError(t, err)
		rc.Close()
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
		assert.Positive(t, srv.deps.ActionsApp.SweepCaches())
		srv.deps.ActionsApp.Now = nil
		var n int64
		srv.deps.Database.Db.Model(&domain.ActionCache{}).Count(&n)
		assert.Zero(t, n, "unused for 7 days")
		_, err = srv.deps.Storage.OpenFile(key, "actions-cache", entry.Id)
		assert.Error(t, err, "the file too")
	})
}
