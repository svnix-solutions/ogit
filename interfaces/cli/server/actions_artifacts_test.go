package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const twirp = "/twirp/github.actions.results.api.v1.ArtifactService/"

// artifactClient speaks the artifact service like @actions/artifact v2.
type artifactClient struct {
	t     *testing.T
	srv   *testServer
	task  *application.Task
	token string
}

func (a *artifactClient) call(method string, body map[string]any) (int, map[string]any) {
	a.t.Helper()
	if _, ok := body["workflow_run_backend_id"]; !ok {
		body["workflow_run_backend_id"] = a.task.RunId
		body["workflow_job_run_backend_id"] = a.task.RunId
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, a.srv.url+twirp+method, bytes.NewReader(data))
	require.NoError(a.t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(a.t, err)
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func put(t *testing.T, url string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	return resp.StatusCode
}

func get(t *testing.T, url string, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	status, header, body := fetch(t, url, headers)
	return status, body, header
}

// upload sends content as blocks, the last first, and commits them in
// order (the Azure SDK uploads blocks in parallel).
func (a *artifactClient) upload(name string, content []byte, blocks int) (int, map[string]any) {
	a.t.Helper()
	status, body := a.call("CreateArtifact", map[string]any{"name": name, "version": 4})
	if status != http.StatusOK {
		return status, body
	}
	url := body["signedUploadUrl"].(string)
	size := (len(content) + blocks - 1) / blocks
	var ids []string
	var parts [][]byte
	for i := 0; i < blocks; i++ {
		ids = append(ids, base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("block-%06d", i))))
		parts = append(parts, content[min(i*size, len(content)):min((i+1)*size, len(content))])
	}
	for i := blocks - 1; i >= 0; i-- {
		if s := put(a.t, url+"&comp=block&blockid="+ids[i], parts[i]); s != http.StatusCreated {
			return s, nil
		}
	}
	list := `<?xml version="1.0" encoding="utf-8"?><BlockList>`
	for _, id := range ids {
		list += "<Latest>" + id + "</Latest>"
	}
	if s := put(a.t, url+"&comp=blocklist", []byte(list+"</BlockList>")); s != http.StatusCreated {
		return s, nil
	}
	return a.call("FinalizeArtifact", map[string]any{"name": name, "size": fmt.Sprint(len(content))})
}

func TestActionsArtifacts(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.Actions.MaxArtifactSize = 1 << 20 })
	srv := f.srv
	r := f.runner(t)
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": workflow("  push:")})
	task := fetch0(t, r, "ubuntu-latest")
	require.NotNil(t, task)
	require.NotZero(t, task.RunApiId)
	client := &artifactClient{t: t, srv: srv, task: task, token: task.RuntimeToken}
	content := bytes.Repeat([]byte("artifact-bytes-"), 20000) // 300 KB

	t.Run("runtime_token", func(t *testing.T) {
		parts := strings.Split(task.RuntimeToken, ".")
		require.Len(t, parts, 3)
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		require.NoError(t, err)
		assert.Contains(t, string(claims), "Actions.Results:"+task.RunId+":"+task.RunId, "clients read the run from scp")
		status, _ := (&artifactClient{t: t, srv: srv, task: task, token: "nope"}).call("ListArtifacts", map[string]any{})
		assert.Equal(t, http.StatusUnauthorized, status)
	})

	t.Run("upload_and_download", func(t *testing.T) {
		status, body := client.upload("dist", content, 3)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotEmpty(t, body["artifactId"])
		status, body = client.call("ListArtifacts", map[string]any{"name_filter": "dist"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		list := body["artifacts"].([]any)
		require.Len(t, list, 1)
		assert.Equal(t, fmt.Sprint(len(content)), list[0].(map[string]any)["size"], "blocks assembled")
		status, body = client.call("GetSignedArtifactURL", map[string]any{"name": "dist"})
		require.Equal(t, http.StatusOK, status)
		status, got, header := get(t, body["signedUrl"].(string), nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "application/zip", header.Get("Content-Type"))
		assert.True(t, bytes.Equal(content, got), "in the block list's order")

		status, _ = client.upload("dist", content, 1)
		assert.Equal(t, http.StatusConflict, status, "names are unique per run")
		status, _ = client.call("CreateArtifact", map[string]any{"name": "../evil"})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("signed_urls", func(t *testing.T) {
		_, body := client.call("GetSignedArtifactURL", map[string]any{"name": "dist"})
		url := body["signedUrl"].(string)
		status, _, _ := get(t, strings.Replace(url, "sig=", "sig=x", 1), nil)
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, http.StatusUnauthorized, put(t, strings.Replace(url, "DownloadArtifact", "UploadArtifact", 1), []byte("x")), "a download URL doesn't upload")
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		status, _, _ = get(t, url, nil)
		srv.deps.ActionsApp.Now = nil
		assert.Equal(t, http.StatusUnauthorized, status, "expired")
	})

	t.Run("too_large", func(t *testing.T) {
		status, _ := client.upload("big", bytes.Repeat([]byte("x"), 1<<20+1), 2)
		assert.Equal(t, http.StatusRequestEntityTooLarge, status)
		_, body := client.call("ListArtifacts", map[string]any{"name_filter": "big"})
		assert.Empty(t, body["artifacts"])
	})

	t.Run("abandoned_uploads", func(t *testing.T) {
		status, body := client.call("CreateArtifact", map[string]any{"name": "half"})
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, http.StatusCreated, put(t, body["signedUploadUrl"].(string)+"&comp=block&blockid=QQ==", []byte("part")))
		var half domain.ActionArtifact
		require.NoError(t, srv.deps.Database.Db.Where("name = ?", "half").First(&half).Error)
		key := srv.repoStorageKey(t, "owner/app")
		block := half.Id + ".block." + fmt.Sprintf("%x", "QQ==")
		rc, err := srv.deps.Storage.OpenFile(key, "artifacts", block)
		require.NoError(t, err, "stored")
		rc.Close()
		assert.Zero(t, srv.deps.ActionsApp.SweepArtifacts(), "not yet")
		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
		assert.Equal(t, 2, srv.deps.ActionsApp.SweepArtifacts(), "half, and the too large one: unfinished for a day")
		srv.deps.ActionsApp.Now = nil
		_, err = srv.deps.Storage.OpenFile(key, "artifacts", block)
		assert.Error(t, err, "its blocks go too")
	})

	t.Run("readers", func(t *testing.T) {
		status, body := srv.get(t, actionsAPI("owner/app")+"/runs/1/artifacts", f.reporter.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		arts := body["artifacts"].([]any)
		require.Len(t, arts, 1)
		art := arts[0].(map[string]any)
		assert.Equal(t, "dist", art["name"])
		zip := srv.url + art["download_url"].(string)
		status, got, _ := get(t, zip, map[string]string{"Authorization": "Bearer " + f.reporter.session})
		require.Equal(t, http.StatusOK, status)
		assert.True(t, bytes.Equal(content, got))
		status, _, _ = get(t, zip, map[string]string{"Authorization": "Bearer " + f.outer.session})
		assert.Equal(t, http.StatusNotFound, status, "private")
	})

	t.Run("other_runs", func(t *testing.T) {
		finish(t, r, task, "success")
		status, _ := client.call("ListArtifacts", map[string]any{})
		assert.Equal(t, http.StatusUnauthorized, status, "the runtime token ends with its run")

		pushFiles(t, f.url, f.owner.git, "main", "again", map[string]string{"x.txt": "x\n"})
		next := fetch0(t, r, "ubuntu-latest")
		require.NotNil(t, next)
		other := &artifactClient{t: t, srv: srv, task: next, token: next.RuntimeToken}
		status, body := other.call("ListArtifacts", map[string]any{"workflow_run_backend_id": task.RunId, "workflow_job_run_backend_id": task.RunId})
		assert.Equal(t, http.StatusNotFound, status, "a run's token lists its own run only: %v", body)

		// download-artifact with run-id and github-token: the REST API.
		h := map[string]string{"Authorization": "token " + next.Token}
		status, body, _ = srv.do(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/owner/app/actions/runs/%d/artifacts?name=dist", task.RunApiId), h, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		require.EqualValues(t, 1, body["total_count"])
		id := int64(body["artifacts"].([]any)[0].(map[string]any)["id"].(float64))
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v3/repos/owner/app/actions/artifacts/%d/zip", srv.url, id), nil)
		req.Header.Set("Authorization", h["Authorization"])
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusFound, resp.StatusCode)
		status, got, _ := get(t, resp.Header.Get("Location"), nil)
		require.Equal(t, http.StatusOK, status, "the redirect works without credentials")
		assert.True(t, bytes.Equal(content, got))
		status, _, _ = srv.do(t, http.MethodGet, fmt.Sprintf("/api/v3/repos/owner/app/actions/artifacts/%d", id), nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "private: no token, no artifact")
		finish(t, r, next, "success")
	})

	t.Run("delete_and_expiry", func(t *testing.T) {
		_, body := srv.get(t, actionsAPI("owner/app")+"/runs/1/artifacts", f.reporter.session)
		id := fmt.Sprint(int64(body["artifacts"].([]any)[0].(map[string]any)["id"].(float64)))
		status, _ := srv.request(t, http.MethodDelete, actionsAPI("owner/app")+"/artifacts/"+id, f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "writers delete")

		var dist domain.ActionArtifact
		require.NoError(t, srv.deps.Database.Db.Where("name = ?", "dist").First(&dist).Error)
		key := srv.repoStorageKey(t, "owner/app")
		rc, err := srv.deps.Storage.OpenFile(key, "artifacts", dist.Id+".zip")
		require.NoError(t, err, "stored")
		rc.Close()

		srv.deps.ActionsApp.Now = func() time.Time { return time.Now().Add(91 * 24 * time.Hour) }
		assert.Equal(t, 1, srv.deps.ActionsApp.SweepArtifacts())
		srv.deps.ActionsApp.Now = nil
		var n int64
		srv.deps.Database.Db.Model(&domain.ActionArtifact{}).Count(&n)
		assert.Zero(t, n, "90 days by default")
		_, err = srv.deps.Storage.OpenFile(key, "artifacts", dist.Id+".zip")
		assert.Error(t, err, "the zip too")
	})
}

func TestActionsArtifactRetention(t *testing.T) {
	f := newPullFixture(t, true, func(c *config.Config) { c.Actions.ArtifactRetentionDays = 10 })
	srv := f.srv
	r := f.runner(t)
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/ci.yml": workflow("  push:")})
	task := fetch0(t, r, "ubuntu-latest")
	require.NotNil(t, task)
	client := &artifactClient{t: t, srv: srv, task: task, token: task.RuntimeToken}
	for name, days := range map[string]int{"short": 3, "long": 30} {
		expires := time.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
		status, body := client.call("CreateArtifact", map[string]any{"name": name, "expires_at": expires})
		require.Equal(t, http.StatusOK, status, "%v", body)
		require.Equal(t, http.StatusCreated, put(t, body["signedUploadUrl"].(string), []byte(name)))
		status, _ = client.call("FinalizeArtifact", map[string]any{"name": name})
		require.Equal(t, http.StatusOK, status)
	}
	var arts []domain.ActionArtifact
	require.NoError(t, srv.deps.Database.Db.Order("name").Find(&arts).Error)
	require.Len(t, arts, 2)
	assert.WithinDuration(t, time.Now().Add(10*24*time.Hour), arts[0].ExpiresAt, time.Hour, "long: capped at the configured 10 days")
	assert.WithinDuration(t, time.Now().Add(3*24*time.Hour), arts[1].ExpiresAt, time.Hour, "short: the workflow's retention-days")
	rc, err := srv.deps.ActionsApp.OpenArtifact(&arts[1])
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	assert.Equal(t, "short", string(data), "a single PUT")
}
