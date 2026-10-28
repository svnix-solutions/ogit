package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fields go-github decodes: ids are integers there, so a string id
// (or a user id that isn't one) fails the whole response.
type ghTestUser struct {
	Login string `json:"login"`
	Id    *int64 `json:"id"`
}

type ghTestAsset struct {
	Id                 int64       `json:"id"`
	Name               string      `json:"name"`
	Label              string      `json:"label"`
	Size               int64       `json:"size"`
	State              string      `json:"state"`
	BrowserDownloadURL string      `json:"browser_download_url"`
	Uploader           *ghTestUser `json:"uploader"`
}

type ghTestRelease struct {
	Id          int64         `json:"id"`
	TagName     string        `json:"tag_name"`
	Name        string        `json:"name"`
	Body        string        `json:"body"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	UploadURL   string        `json:"upload_url"`
	AssetsURL   string        `json:"assets_url"`
	PublishedAt *time.Time    `json:"published_at"`
	Author      *ghTestUser   `json:"author"`
	Assets      []ghTestAsset `json:"assets"`
}

func (s *testServer) gh(t *testing.T, method, path string, headers map[string]string, body any, out any) int {
	t.Helper()
	var reader io.Reader
	if b, ok := body.([]byte); ok {
		reader = bytes.NewReader(b)
	} else if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	}
	status, _, raw, _ := s.send(t, method, path, headers, reader)
	if out != nil && status < 300 {
		require.NoError(t, json.Unmarshal(raw, out), "decodes like go-github: %s", raw)
	}
	return status
}

// A goreleaser-like run: find the release by tag, create a draft with
// generated notes, upload, publish.
func TestGitHubReleasesAPI(t *testing.T) {
	f := newPullFixture(t, true, withPublicURL)
	srv := f.srv
	status, pr := f.open(t, f.dev, "feature", map[string]string{"f.txt": "1\n"}, map[string]any{"title": "Add F"})
	require.Equal(t, http.StatusOK, status, "%v", pr)
	status, body := srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	tok := srv.createToken(t, f.dev.session, "goreleaser", "read:repository", "write:repository").Password
	h := tokenHeader(tok)
	base := "/api/v3/repos/owner/app/releases"
	local := func(u string) string { return strings.TrimPrefix(u, "https://git.example.com") }

	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, base+"/tags/v2.0.0", h, nil, nil))
	assert.Equal(t, http.StatusUnauthorized, srv.gh(t, http.MethodPost, base, nil, map[string]any{"tag_name": "v2.0.0"}, nil))

	var rel ghTestRelease
	require.Equal(t, http.StatusCreated, srv.gh(t, http.MethodPost, base, h, map[string]any{
		"tag_name": "v2.0.0", "target_commitish": "main", "draft": true, "generate_release_notes": true,
	}, &rel))
	assert.Positive(t, rel.Id)
	assert.Less(t, rel.Id, int64(1)<<53, "safe in JavaScript")
	assert.True(t, rel.Draft)
	assert.Equal(t, "v2.0.0", rel.Name)
	assert.Contains(t, rel.Body, "* Add F by @dev in #1")
	assert.Equal(t, "dev", rel.Author.Login)
	assert.Nil(t, rel.Author.Id)
	id := strconv.FormatInt(rel.Id, 10)
	assert.Equal(t, "https://git.example.com/api/uploads/repos/owner/app/releases/"+id+"/assets{?name,label}", rel.UploadURL)
	assert.Equal(t, http.StatusUnprocessableEntity, srv.gh(t, http.MethodPost, base, h, map[string]any{"tag_name": "v2.0.0"}, nil), "one release per tag")

	// Upload to the upload URL (GitHub Enterprise's), as go-github does.
	upload := local(strings.TrimSuffix(rel.UploadURL, "{?name,label}"))
	bin := []byte("binary contents")
	var asset ghTestAsset
	up := map[string]string{"Authorization": "token " + tok, "Content-Type": "application/zip"}
	require.Equal(t, http.StatusCreated, srv.gh(t, http.MethodPost, upload+"?name=app_linux_amd64.zip&label=Linux", up, bin, &asset))
	assert.Positive(t, asset.Id)
	assert.Equal(t, "app_linux_amd64.zip", asset.Name)
	assert.Equal(t, "Linux", asset.Label)
	assert.Equal(t, "uploaded", asset.State)
	assert.EqualValues(t, len(bin), asset.Size)
	assert.Equal(t, http.StatusUnprocessableEntity, srv.gh(t, http.MethodPost, upload+"?name=app_linux_amd64.zip", up, bin, nil),
		"goreleaser replaces an existing asset on 422")
	require.Equal(t, http.StatusCreated, srv.gh(t, http.MethodPost, base+"/"+id+"/assets?name=checksums.txt", up, []byte("sum\n"), nil), "also under /api/v3")

	var assets []ghTestAsset
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"/"+id+"/assets", h, nil, &assets))
	assert.Len(t, assets, 2)

	assetPath := base + "/assets/" + strconv.FormatInt(asset.Id, 10)
	status, _, got, _ := srv.send(t, http.MethodGet, assetPath, map[string]string{"Authorization": "token " + tok, "Accept": "application/octet-stream"}, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, bin, got, "the file with Accept: application/octet-stream")
	var meta ghTestAsset
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, assetPath, h, nil, &meta))
	assert.Equal(t, asset.Id, meta.Id)
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodPatch, assetPath, h, map[string]any{"label": "Linux x64"}, &meta))
	assert.Equal(t, "Linux x64", meta.Label)

	// Publish: the tag is created.
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodPatch, base+"/"+id, h, map[string]any{"draft": false}, &rel))
	assert.False(t, rel.Draft)
	assert.NotNil(t, rel.PublishedAt)
	assert.NotEmpty(t, srv.refs(t, f.owner, "owner/app")["tags/v2.0.0"])
	assert.Equal(t, "https://git.example.com/owner/app/releases/download/v2.0.0/app_linux_amd64.zip", rel.Assets[0].BrowserDownloadURL)

	var latest ghTestRelease
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"/latest", h, nil, &latest))
	assert.Equal(t, rel.Id, latest.Id)
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"/tags/v2.0.0", h, nil, &latest))
	assert.Equal(t, rel.Id, latest.Id)
	var list []ghTestRelease
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"?per_page=1&page=1", h, nil, &list))
	assert.Len(t, list, 1)
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"?per_page=1&page=2", h, nil, &list))
	assert.Empty(t, list)

	var notes map[string]any
	require.Equal(t, http.StatusOK, srv.gh(t, http.MethodPost, base+"/generate-notes", h, map[string]any{"tag_name": "v2.1.0"}, &notes))
	assert.Contains(t, notes["body"], "v2.0.0...v2.1.0")

	// Others: readers read, outsiders see nothing, bad ids are 404s.
	reader := tokenHeader(srv.createToken(t, f.reporter.session, "r", "read:repository", "write:repository").Password)
	assert.Equal(t, http.StatusOK, srv.gh(t, http.MethodGet, base+"/"+id, reader, nil, nil))
	assert.Equal(t, http.StatusForbidden, srv.gh(t, http.MethodPost, base, reader, map[string]any{"tag_name": "v9"}, nil))
	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, base+"/"+id, tokenHeader(srv.createToken(t, f.outer.session, "o", "read:repository").Password), nil, nil))
	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, base+"/"+rel.TagName, h, nil, nil))
	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, base+"/assets/12", h, nil, nil))

	assert.Equal(t, http.StatusNoContent, srv.gh(t, http.MethodDelete, assetPath, h, nil, nil))
	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, assetPath, h, nil, nil))
	assert.Equal(t, http.StatusNoContent, srv.gh(t, http.MethodDelete, base+"/"+id, h, nil, nil))
	assert.Equal(t, http.StatusNotFound, srv.gh(t, http.MethodGet, base+"/"+id, h, nil, nil))
}
