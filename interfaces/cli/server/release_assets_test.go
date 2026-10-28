package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hideLength makes the client send a chunked body (no Content-Length).
type hideLength struct{ io.Reader }

// send sends body as is (a file, not JSON) and decodes a JSON answer.
func (s *testServer) send(t *testing.T, method, path string, headers map[string]string, body io.Reader) (int, map[string]any, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, body)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, raw, resp.Header
}

// tooLarge sends an oversized body: the server answers 413 and closes the
// connection, possibly before the client is done writing (then the client
// sees a broken pipe or a reset instead of the answer).
func (s *testServer) tooLarge(t *testing.T, method, path string, headers map[string]string, body io.Reader) bool {
	t.Helper()
	req, err := http.NewRequest(method, s.url+path, body)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return strings.Contains(err.Error(), "broken pipe") || strings.Contains(err.Error(), "connection reset")
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusRequestEntityTooLarge
}

func bearer(as user) map[string]string {
	return map[string]string{"Authorization": "Bearer " + as.session}
}

func (s *testServer) uploadAsset(t *testing.T, headers map[string]string, repo, releaseId, name string, content []byte) (int, map[string]any) {
	t.Helper()
	status, body, _, _ := s.send(t, http.MethodPost, releasesAPI(repo, releaseId, "assets")+"?name="+name, headers, bytes.NewReader(content))
	return status, body
}

func withMaxAssetSize(n int64) func(*config.Config) {
	return func(c *config.Config) { c.Releases.MaxAssetSize = n }
}

func TestReleaseAssets(t *testing.T) {
	t.Run("local", func(t *testing.T) { testReleaseAssets(t) })
	t.Run("s3_fake", func(t *testing.T) { testReleaseAssets(t, withS3(s3test.StartFake(t))) })
}

func testReleaseAssets(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, append(opts, allowLoopback, withMaxAssetSize(6<<20))...)
	srv := f.srv
	pushTag(t, f.url, f.dev, "main", "v1.0.0")
	rel := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v1.0.0"})
	relId := rel["id"].(string)
	content := []byte("#!/bin/sh\necho hello\n")

	var asset, safe map[string]any
	t.Run("upload", func(t *testing.T) {
		status, body := srv.uploadAsset(t, bearer(f.dev), "owner/app", relId, "app.sh", content)
		require.Equal(t, http.StatusOK, status, "%v", body)
		asset = body
		assert.Equal(t, "app.sh", body["name"])
		assert.EqualValues(t, len(content), body["size"])
		assert.EqualValues(t, 0, body["download_count"])
		assert.Equal(t, "dev", body["uploader"])
		assert.Equal(t, "/owner/app/releases/download/v1.0.0/app.sh", body["browser_download_url"])

		status, body = srv.uploadAsset(t, bearer(f.dev), "owner/app", relId, "app.sh", content)
		assert.Equal(t, http.StatusUnprocessableEntity, status, "names are unique per release: %v", body)

		status, body = srv.uploadAsset(t, bearer(f.dev), "owner/app", relId, "..%2F..%2Fetc%2Fpasswd", content)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "etc.passwd", body["name"], "made safe")
		safe = body
		status, body = srv.uploadAsset(t, bearer(f.dev), "owner/app", relId, "", content)
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)

		status, _ = srv.uploadAsset(t, bearer(f.reporter), "owner/app", relId, "r.txt", content)
		assert.Equal(t, http.StatusForbidden, status, "readers don't upload")
		status, _ = srv.uploadAsset(t, bearer(f.outer), "owner/app", relId, "o.txt", content)
		assert.Equal(t, http.StatusNotFound, status, "private stays hidden")
		status, _ = srv.uploadAsset(t, nil, "owner/app", relId, "a.txt", content)
		assert.Equal(t, http.StatusNotFound, status)

		_, r := srv.get(t, releasesAPI("owner/app", relId), f.reporter.session)
		assert.Len(t, r["assets"], 2, "listed with the release")
	})

	t.Run("download", func(t *testing.T) {
		url := strings.TrimPrefix(asset["url"].(string), srv.url)
		status, _, got, h := srv.send(t, http.MethodGet, url, bearer(f.reporter), nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, content, got)
		assert.Equal(t, "application/octet-stream", h.Get("Content-Type"), "never the uploaded type")
		assert.Contains(t, h.Get("Content-Disposition"), `attachment; filename=app.sh`)
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))

		// The public link, with git's auth (a token for a private repository).
		link := asset["browser_download_url"].(string)
		status, _, got, _ = srv.send(t, http.MethodGet, link, basic(f.reporter.git), nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, content, got)
		status, _, _, _ = srv.send(t, http.MethodGet, link, nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "anonymous: a private repository is hidden")
		status, _, _, _ = srv.send(t, http.MethodGet, "/owner/app/releases/download/v1.0.0/nope", basic(f.reporter.git), nil)
		assert.Equal(t, http.StatusNotFound, status)

		_, a := srv.get(t, releasesAPI("owner/app", relId, "assets", asset["id"].(string)), f.reporter.session)
		assert.EqualValues(t, 2, a["download_count"], "every download counts")

		html := map[string]string{"Authorization": "Bearer " + f.dev.session, "Content-Type": "text/html"}
		status, body := srv.uploadAsset(t, html, "owner/app", relId, "page.html", []byte("<script>alert(1)</script>"))
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "text/html", body["content_type"], "the declared type is kept as information")
		_, _, _, h = srv.send(t, http.MethodGet, strings.TrimPrefix(body["url"].(string), srv.url), bearer(f.dev), nil)
		assert.Equal(t, "application/octet-stream", h.Get("Content-Type"), "HTML is never served as HTML")
	})

	t.Run("streamed_and_limited", func(t *testing.T) {
		big := bytes.Repeat([]byte("0123456789abcdef"), 5<<16) // 5 MiB: over fiber's 4 MiB body limit
		status, body := srv.uploadAsset(t, bearer(f.dev), "owner/app", relId, "big.bin", big)
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, _, got, _ := srv.send(t, http.MethodGet, strings.TrimPrefix(body["url"].(string), srv.url), bearer(f.dev), nil)
		assert.Equal(t, len(big), len(got))
		assert.True(t, bytes.Equal(big, got))

		huge := bytes.Repeat([]byte("x"), 7<<20)
		upload := releasesAPI("owner/app", relId, "assets") + "?name=huge.bin"
		assert.True(t, srv.tooLarge(t, http.MethodPost, upload, bearer(f.dev), bytes.NewReader(huge)), "Content-Length over the limit")
		assert.True(t, srv.tooLarge(t, http.MethodPost, upload, bearer(f.dev), hideLength{bytes.NewReader(huge)}), "chunked, counted while streaming")
		_, r := srv.get(t, releasesAPI("owner/app", relId, "assets"), f.dev.session)
		for _, a := range r["assets"].([]any) {
			assert.NotEqual(t, "huge.bin", a.(map[string]any)["name"])
		}
		if dir := srv.deps.Config.Storage.Local.Path; srv.deps.Config.Storage.Type == "local" {
			files, _ := filepath.Glob(filepath.Join(dir, srv.repoStorageKey(t, "owner/app")+".git", "release-assets", "*"))
			assert.Len(t, files, len(r["assets"].([]any)), "the refused files are gone")
		}

		// Other routes keep the usual limit although bodies are streamed.
		assert.True(t, srv.tooLarge(t, http.MethodPost, issuesAPI("owner/app"), bearer(f.dev), bytes.NewReader(big)))
		assert.True(t, srv.tooLarge(t, http.MethodPost, issuesAPI("owner/app"), bearer(f.dev), hideLength{bytes.NewReader(big)}), "chunked too")
		small, _ := json.Marshal(map[string]any{"title": "chunked"})
		status, body, _, _ = srv.send(t, http.MethodPost, issuesAPI("owner/app"), map[string]string{"Authorization": "Bearer " + f.dev.session, "Content-Type": "application/json"}, hideLength{bytes.NewReader(small)})
		assert.Equal(t, http.StatusOK, status, "small chunked bodies still work: %v", body)
	})

	t.Run("rename_and_delete", func(t *testing.T) {
		path := releasesAPI("owner/app", relId, "assets", asset["id"].(string))
		status, body := srv.request(t, http.MethodPatch, path, f.dev.session, map[string]any{"name": "page.html"})
		assert.Equal(t, http.StatusUnprocessableEntity, status, "taken: %v", body)
		status, body = srv.request(t, http.MethodPatch, path, f.reporter.session, map[string]any{"label": "x"})
		assert.Equal(t, http.StatusForbidden, status, "%v", body)
		status, body = srv.request(t, http.MethodPatch, path, f.dev.session, map[string]any{"name": "app-linux.sh", "label": "Linux"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "app-linux.sh", body["name"])
		assert.Equal(t, "Linux", body["label"])

		status, _ = srv.request(t, http.MethodDelete, path, f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, path, f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, path, f.dev.session)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _, _ = srv.send(t, http.MethodGet, "/owner/app/releases/download/v1.0.0/app-linux.sh", basic(f.dev.git), nil)
		assert.Equal(t, http.StatusNotFound, status)

		other := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v-other", "draft": true})
		status, _ = srv.get(t, releasesAPI("owner/app", relId, "assets", safe["id"].(string)), f.dev.session)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, releasesAPI("owner/app", other["id"].(string), "assets", safe["id"].(string)), f.dev.session)
		assert.Equal(t, http.StatusNotFound, status, "an asset only through its own release")
	})

	t.Run("drafts", func(t *testing.T) {
		rcv := newHookReceiver(t)
		srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"release"}})
		srv.deliver(t)
		rcv.take()

		draft := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v2.0.0", "draft": true})
		id := draft["id"].(string)
		status, body := srv.uploadAsset(t, bearer(f.dev), "owner/app", id, "draft.bin", content)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Nil(t, body["browser_download_url"], "no public link before publishing")
		status, _ = srv.get(t, releasesAPI("owner/app", id, "assets", body["id"].(string)), f.reporter.session)
		assert.Equal(t, http.StatusNotFound, status, "drafts' assets stay between writers")
		status, _, _, _ = srv.send(t, http.MethodGet, strings.TrimPrefix(body["url"].(string), srv.url), bearer(f.reporter), nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _, _ = srv.send(t, http.MethodGet, "/owner/app/releases/download/v2.0.0/draft.bin", basic(f.dev.git), nil)
		assert.Equal(t, http.StatusNotFound, status, "the public link waits for publishing, even for writers")

		status, body = srv.request(t, http.MethodPatch, releasesAPI("owner/app", id), f.dev.session, map[string]any{"draft": false})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _, _, _ = srv.send(t, http.MethodGet, "/owner/app/releases/download/v2.0.0/draft.bin", basic(f.reporter.git), nil)
		assert.Equal(t, http.StatusOK, status)
		srv.deliver(t)
		published := find(t, rcv.takeEvents(), "release.published").payload["release"].(map[string]any)
		assets := published["assets"].([]any)
		require.Len(t, assets, 1, "the payload lists the assets")
		assert.Equal(t, "draft.bin", assets[0].(map[string]any)["name"])
		assert.Equal(t, "dev", assets[0].(map[string]any)["uploader"].(map[string]any)["login"])
	})

	t.Run("tokens", func(t *testing.T) {
		ci := srv.repoToken(t, f.maint, "owner/app", "ci", "read:repository", "write:repository")
		status, body := srv.uploadAsset(t, tokenHeader(ci["token"].(string)), "owner/app", relId, "ci.tar.gz", content)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Nil(t, body["uploader"], "no person")
		ro := srv.repoToken(t, f.maint, "owner/app", "ro", "read:repository")
		status, _ = srv.uploadAsset(t, tokenHeader(ro["token"].(string)), "owner/app", relId, "ro.tar.gz", content)
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("deleting_the_release_deletes_its_assets", func(t *testing.T) {
		r := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v3.0.0"})
		status, body := srv.uploadAsset(t, bearer(f.dev), "owner/app", r["id"].(string), "gone.bin", content)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.request(t, http.MethodDelete, releasesAPI("owner/app", r["id"].(string)), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		var n int64
		require.NoError(t, srv.deps.Database.Db.Table("release_asset").Where("id = ?", body["id"]).Count(&n).Error)
		assert.Zero(t, n)
		_, err := srv.deps.Storage.OpenAsset(srv.repoStorageKey(t, "owner/app"), body["id"].(string))
		assert.Error(t, err, "the file too")
	})
}

func (s *testServer) repoStorageKey(t *testing.T, path string) string {
	t.Helper()
	parts := strings.SplitN(path, "/", 2)
	var key string
	require.NoError(t, s.deps.Database.Db.Table("repository").Select("storage_key").Where("namespace = ? AND slug = ?", parts[0], parts[1]).Scan(&key).Error)
	return key
}
