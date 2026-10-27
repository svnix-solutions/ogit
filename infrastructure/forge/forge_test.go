package forge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIURL(t *testing.T) {
	for _, c := range []struct{ kind, remote, api, project string }{
		{GitHub, "https://github.com/octo/app.git", "https://api.github.com", "octo/app"},
		{GitHub, "git@github.com:octo/app.git", "https://api.github.com", "octo/app"},
		{GitHub, "https://ghe.example.com/octo/app", "https://ghe.example.com/api/v3", "octo/app"},
		{GitLab, "https://gitlab.com/group/sub/app.git", "https://gitlab.com/api/v4", "group/sub/app"},
		{GitLab, "ssh://git@gitlab.example.com:2222/group/app.git", "https://gitlab.example.com/api/v4", "group/app"},
		{Gitea, "http://gitea.local:3000/octo/app.git", "http://gitea.local:3000/api/v1", "octo/app"},
	} {
		api, project, err := APIURL(c.kind, c.remote)
		require.NoError(t, err, c.remote)
		assert.Equal(t, c.api, api, c.remote)
		assert.Equal(t, c.project, project, c.remote)
	}
	_, _, err := APIURL(GitHub, "not a url")
	assert.Error(t, err)
}

func TestNew(t *testing.T) {
	_, err := New(Options{Kind: "bitbucket", APIURL: "https://x", Project: "a/b"})
	assert.Error(t, err)
	_, err = New(Options{Kind: GitHub, APIURL: "ftp://x", Project: "a/b"})
	assert.Error(t, err)
	_, err = New(Options{Kind: GitHub, APIURL: "https://x", Project: "nope"})
	assert.Error(t, err, "owner/name")
	src, err := New(Options{Kind: GitLab, APIURL: "https://gitlab.com/api/v4/", Project: "group/sub/app.git"})
	require.NoError(t, err)
	assert.Equal(t, "https://gitlab.com/api/v4/projects/group%2Fsub%2Fapp/labels", src.(*gitlab).c.url(src.(*gitlab).project+"/labels", nil))
}

func TestNextLink(t *testing.T) {
	assert.Equal(t, "https://api.github.com/x?page=2",
		nextLink(`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=9>; rel="last"`))
	assert.Equal(t, "https://h/x?page=3", nextLink(`<https://h/x?page=1>; rel="prev", <https://h/x?page=3>; rel=next`))
	assert.Equal(t, "", nextLink(`<https://h/x?page=1>; rel="first"`))
	assert.Equal(t, "", nextLink(""))
}

func TestRateLimited(t *testing.T) {
	resp := func(status int, h map[string]string) *http.Response {
		r := &http.Response{StatusCode: status, Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	until, ok := rateLimited(resp(403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "2000000000"}))
	assert.True(t, ok)
	assert.Equal(t, time.Unix(2000000000, 0), until)
	until, ok = rateLimited(resp(429, map[string]string{"Retry-After": "30"}))
	assert.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(30*time.Second), until, 5*time.Second)
	_, ok = rateLimited(resp(403, map[string]string{"X-RateLimit-Remaining": "12"}))
	assert.False(t, ok, "a plain 403")
	_, ok = rateLimited(resp(500, nil))
	assert.False(t, ok)
}

func TestPullRefs(t *testing.T) {
	gh, gl := PullRefs(GitHub), PullRefs(GitLab)
	assert.True(t, gh("refs/pull/12/head"))
	assert.False(t, gh("refs/pull/12/merge"))
	assert.False(t, gh("refs/pull/x/head"))
	assert.False(t, gh("refs/merge-requests/1/head"))
	assert.True(t, gl("refs/merge-requests/1/head"))
	assert.False(t, gl("refs/pull/1/head"))
}

func TestColor(t *testing.T) {
	assert.Equal(t, "#d73a4a", color("D73A4A"))
	assert.Equal(t, "#0e8a16", color("#0e8a16"))
	assert.Equal(t, "#cccccc", color("red"))
}

// The token goes to the API's host only: never to another host serving a
// download (an asset URL, or a redirect).
func TestCredentialsStayOnTheAPIHost(t *testing.T) {
	var elsewhere string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere = r.Header.Get("Authorization") + r.Header.Get("PRIVATE-TOKEN")
		_, _ = w.Write([]byte("data"))
	}))
	defer other.Close()
	var onAPI string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		onAPI = r.Header.Get("Authorization")
		http.Redirect(w, r, other.URL+"/blob", http.StatusFound)
	}))
	defer api.Close()
	src, err := New(Options{Kind: GitHub, APIURL: api.URL, Project: "a/b", Token: "secret", Client: &http.Client{}})
	require.NoError(t, err)
	for _, href := range []string{api.URL + "/repos/a/b/releases/assets/1", other.URL + "/direct"} {
		elsewhere = ""
		r, err := src.Download(context.Background(), Asset{URL: href})
		require.NoError(t, err)
		r.Close()
		assert.Empty(t, elsewhere, href)
	}
	assert.Equal(t, "Bearer secret", onAPI)
}
