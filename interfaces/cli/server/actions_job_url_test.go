package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestActionsLinksFollowTheJobsAddress: with a public URL set, a job that
// reaches the server on another address (ogit runner run --job-url) gets
// artifact and cache links on that address, and they work.
func TestActionsLinksFollowTheJobsAddress(t *testing.T) {
	f := newPullFixture(t, true, withPublicURL)
	r := f.runner(t)
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": workflow("  push:")})
	task := fetch0(t, r, "ubuntu-latest")
	require.NotNil(t, task)
	assert.Equal(t, "https://git.example.com", task.ServerURL, "the task still names the public URL")

	// The job calls the server at srv.url, not at the public host.
	a := &artifactClient{t: t, srv: f.srv, task: task, token: task.RuntimeToken}
	content := bytes.Repeat([]byte("a"), 1000)
	status, body := a.upload("dist", content, 1)
	require.Equal(t, http.StatusOK, status, "the signed upload URL works from the job's address: %v", body)
	status, body = a.call("GetSignedArtifactURL", map[string]any{"name": "dist"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	url := body["signedUrl"].(string)
	assert.True(t, strings.HasPrefix(url, f.srv.url+"/"), url)
	status, got, _ := get(t, url, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, content, got)

	c := &cacheClient{t: t, srv: f.srv, token: task.RuntimeToken}
	require.Equal(t, http.StatusNoContent, c.save("deps", "v1", content, 1000))
	status, hit := c.lookup("v1", "deps")
	require.Equal(t, http.StatusOK, status, "%v", hit)
	assert.True(t, strings.HasPrefix(hit["archiveLocation"].(string), f.srv.url+"/"), hit["archiveLocation"])
	restored, _ := c.restore("v1", "deps")
	assert.Equal(t, content, restored)
}
