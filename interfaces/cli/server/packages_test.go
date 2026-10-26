package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// registryClient speaks the OCI distribution protocol like docker does:
// a request, the Bearer challenge, a token from the realm, the request
// again.
type registryClient struct {
	t     *testing.T
	srv   *testServer
	auth  *githttp.BasicAuth // nil: anonymous
	token string
}

func (s *testServer) registry(t *testing.T, auth *githttp.BasicAuth) *registryClient {
	return &registryClient{t: t, srv: s, auth: auth}
}

func (r *registryClient) login() int {
	req, err := http.NewRequest(http.MethodGet, r.srv.url+"/v2/token?service=x&scope=repository:a/b:pull", nil)
	require.NoError(r.t, err)
	if r.auth != nil {
		req.SetBasicAuth(r.auth.Username, r.auth.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(r.t, err)
	defer resp.Body.Close()
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	r.token = body.Token
	return resp.StatusCode
}

func (r *registryClient) do(method, path string, headers map[string]string, body []byte) (*http.Response, []byte) {
	r.t.Helper()
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(method, r.srv.url+path, bytes.NewReader(body))
		require.NoError(r.t, err)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(r.t, err)
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			require.True(r.t, strings.HasPrefix(challenge, `Bearer realm="`+r.srv.url+`/v2/token"`), challenge)
			if r.login() != http.StatusOK || (r.auth == nil && strings.Contains(challenge, "scope=")) {
				return resp, data // anonymous: no use retrying
			}
			continue
		}
		return resp, data
	}
}

// pushBlob uploads a blob: monolithic, or in chunks.
func (r *registryClient) pushBlob(image string, blob []byte, chunks int) int {
	r.t.Helper()
	digest := digestOf(blob)
	if chunks == 0 {
		resp, _ := r.do(http.MethodPost, "/v2/"+image+"/blobs/uploads/?digest="+digest, nil, blob)
		return resp.StatusCode
	}
	resp, body := r.do(http.MethodPost, "/v2/"+image+"/blobs/uploads/", nil, nil)
	if resp.StatusCode != http.StatusAccepted {
		return resp.StatusCode
	}
	require.Equal(r.t, http.StatusAccepted, resp.StatusCode, "%s", body)
	location := resp.Header.Get("Location")
	size := (len(blob) + chunks - 1) / chunks
	for off := 0; off < len(blob); off += size {
		end := min(off+size, len(blob))
		resp, body = r.do(http.MethodPatch, location, map[string]string{"Content-Range": strings.Join([]string{itoa(off), itoa(end - 1)}, "-"), "Content-Type": "application/octet-stream"}, blob[off:end])
		require.Equal(r.t, http.StatusAccepted, resp.StatusCode, "%s", body)
		require.Equal(r.t, "0-"+itoa(end-1), resp.Header.Get("Range"))
		location = resp.Header.Get("Location")
	}
	resp, _ = r.do(http.MethodPut, location+"?digest="+digest, nil, nil)
	return resp.StatusCode
}

type testImage struct {
	config, layer, manifest []byte
	digest                  string
}

func newTestImage(t *testing.T, content string) testImage {
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]},"config":{"Env":["X=` + content + `"]}}`)
	layer := bytes.Repeat([]byte(content), 1000)
	m := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config":        map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(config), "size": len(config)},
		"layers":        []any{map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": digestOf(layer), "size": len(layer)}},
	}
	manifest, err := json.Marshal(m)
	require.NoError(t, err)
	return testImage{config: config, layer: layer, manifest: manifest, digest: digestOf(manifest)}
}

func (r *registryClient) push(image, tag string, img testImage) {
	r.t.Helper()
	require.Equal(r.t, http.StatusCreated, r.pushBlob(image, img.config, 0))
	require.Equal(r.t, http.StatusCreated, r.pushBlob(image, img.layer, 3))
	resp, body := r.do(http.MethodPut, "/v2/"+image+"/manifests/"+tag, map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"}, img.manifest)
	require.Equal(r.t, http.StatusCreated, resp.StatusCode, "%s", body)
	require.Equal(r.t, img.digest, resp.Header.Get("Docker-Content-Digest"))
}

// pull fetches a manifest and its blobs, returning the first failing
// status (or 200).
func (r *registryClient) pull(image, ref string) int {
	r.t.Helper()
	resp, body := r.do(http.MethodGet, "/v2/"+image+"/manifests/"+ref, map[string]string{"Accept": "application/vnd.oci.image.manifest.v1+json"}, nil)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode
	}
	var m struct {
		Config struct{ Digest string }
		Layers []struct{ Digest string }
	}
	require.NoError(r.t, json.Unmarshal(body, &m))
	for _, d := range append([]string{m.Config.Digest}, m.Layers[0].Digest) {
		resp, data := r.do(http.MethodGet, "/v2/"+image+"/blobs/"+d, nil, nil)
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode
		}
		require.Equal(r.t, d, digestOf(data))
	}
	return http.StatusOK
}

func registryError(body []byte) string {
	var e struct {
		Errors []struct{ Code string }
	}
	_ = json.Unmarshal(body, &e)
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code
}

func TestContainerRegistry(t *testing.T) {
	t.Run("local", func(t *testing.T) { testContainerRegistry(t) })
	t.Run("s3_fake", func(t *testing.T) { testContainerRegistry(t, withS3(s3test.StartFake(t))) })
}

func testContainerRegistry(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	aliceW := srv.createToken(t, alice.session, "docker", "write:packages")
	aliceR := srv.createToken(t, alice.session, "pull", "read:packages")
	bobW := srv.createToken(t, bob.session, "docker", "write:packages")
	img := newTestImage(t, "one")

	t.Run("ping", func(t *testing.T) {
		resp, err := http.Get(srv.url + "/v2/")
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `Bearer realm="`+srv.url+`/v2/token"`)
		assert.Equal(t, "registry/2.0", resp.Header.Get("Docker-Distribution-API-Version"))

		c := srv.registry(t, aliceW)
		resp2, _ := c.do(http.MethodGet, "/v2/", nil, nil)
		assert.Equal(t, http.StatusOK, resp2.StatusCode, "docker login")
		bad := srv.registry(t, &githttp.BasicAuth{Username: "alice", Password: "ogit_" + strings.Repeat("0", 64)})
		assert.Equal(t, http.StatusUnauthorized, bad.login(), "a wrong token")
		assert.Equal(t, http.StatusOK, srv.registry(t, nil).login(), "anonymous tokens exist (public images)")
	})

	c := srv.registry(t, aliceW)
	t.Run("push", func(t *testing.T) {
		c.push("alice/app", "v1", img)
		resp, body := c.do(http.MethodGet, "/v2/alice/app/tags/list", nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.JSONEq(t, `{"name":"alice/app","tags":["v1"]}`, string(body))
		assert.Equal(t, http.StatusOK, c.pull("alice/app", "v1"))
		assert.Equal(t, http.StatusOK, c.pull("alice/app", img.digest))

		resp, _ = c.do(http.MethodHead, "/v2/alice/app/manifests/v1", nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, img.digest, resp.Header.Get("Docker-Content-Digest"))
		assert.Equal(t, "application/vnd.oci.image.manifest.v1+json", resp.Header.Get("Content-Type"))
		resp, _ = c.do(http.MethodHead, "/v2/alice/app/blobs/"+digestOf(img.layer), nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, itoa(len(img.layer)), resp.Header.Get("Content-Length"))
	})

	t.Run("invalid_pushes", func(t *testing.T) {
		resp, body := c.do(http.MethodPost, "/v2/alice/app/blobs/uploads/?digest="+digestOf([]byte("other")), nil, []byte("data"))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		assert.Equal(t, "DIGEST_INVALID", registryError(body))

		resp, _ = c.do(http.MethodPost, "/v2/alice/app/blobs/uploads/", nil, nil)
		location := resp.Header.Get("Location")
		resp, _ = c.do(http.MethodPatch, location, map[string]string{"Content-Range": "5-9"}, []byte("12345"))
		assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, resp.StatusCode, "chunks in order")

		other := newTestImage(t, "missing")
		resp, body = c.do(http.MethodPut, "/v2/alice/app/manifests/v2", map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"}, other.manifest)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "BLOB_UNKNOWN", registryError(body), "blobs first")
		resp, body = c.do(http.MethodPut, "/v2/alice/app/manifests/"+digestOf([]byte("x")), map[string]string{"Content-Type": "application/vnd.oci.image.manifest.v1+json"}, img.manifest)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "a digest reference must match")
		resp, _ = c.do(http.MethodPut, "/v2/alice/App/manifests/v1", nil, img.manifest)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "lowercase names")
	})

	t.Run("access", func(t *testing.T) {
		anon := srv.registry(t, nil)
		resp, _ := anon.do(http.MethodGet, "/v2/alice/app/manifests/v1", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "private: anonymous callers are asked to log in")
		assert.Contains(t, resp.Header.Get("WWW-Authenticate"), `scope="repository:alice/app:pull"`)

		b := srv.registry(t, bobW)
		assert.Equal(t, http.StatusNotFound, b.pull("alice/app", "v1"), "bob can't see it")
		resp, body := b.do(http.MethodPost, "/v2/alice/app/blobs/uploads/", nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "nor push to it: %s", body)
		resp, _ = b.do(http.MethodHead, "/v2/bob/app/blobs/"+digestOf(img.layer), nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "blobs are per namespace")
		resp, _ = b.do(http.MethodPost, "/v2/bob/app/blobs/uploads/?mount="+digestOf(img.layer)+"&from=alice/app", nil, nil)
		assert.Equal(t, http.StatusAccepted, resp.StatusCode, "no mount from another namespace: a plain upload starts")

		r := srv.registry(t, aliceR)
		assert.Equal(t, http.StatusOK, r.pull("alice/app", "v1"), "read:packages pulls")
		resp, body = r.do(http.MethodPost, "/v2/alice/app/blobs/uploads/", nil, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "but doesn't push")
		assert.Equal(t, "DENIED", registryError(body))
		git := srv.registry(t, alice.git)
		assert.Equal(t, http.StatusNotFound, git.pull("alice/app", "v1"), "repository scopes don't read packages")

		status, body2 := srv.request(t, http.MethodPatch, "/api/v1/packages/alice/container/app", alice.session, map[string]any{"visibility": "public"})
		require.Equal(t, http.StatusOK, status, "%v", body2)
		assert.Equal(t, http.StatusOK, srv.registry(t, nil).pull("alice/app", "v1"), "public: anyone pulls")
		assert.Equal(t, http.StatusOK, b.pull("alice/app", "v1"))
		resp, _ = srv.registry(t, nil).do(http.MethodHead, "/v2/alice/app/blobs/"+digestOf([]byte("unrelated")), nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp, _ = b.do(http.MethodPut, "/v2/alice/app/manifests/v9", nil, img.manifest)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode, "public is read-only for others")
		resp, _ = b.do(http.MethodPost, "/v2/bob/app/blobs/uploads/?mount="+digestOf(img.layer)+"&from=alice/app", nil, nil)
		assert.Equal(t, http.StatusAccepted, resp.StatusCode, "readable, but blobs are not shared across namespaces")
		resp, _ = c.do(http.MethodPost, "/v2/alice/copy/blobs/uploads/?mount="+digestOf(img.layer)+"&from=alice/app", nil, nil)
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "mounted within the namespace")
	})

	var idx []byte
	t.Run("index", func(t *testing.T) {
		idx, _ = json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
			"manifests": []any{map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": img.digest, "size": len(img.manifest)}}})
		resp, body := c.do(http.MethodPut, "/v2/alice/app/manifests/multi", map[string]string{"Content-Type": "application/vnd.oci.image.index.v1+json"}, idx)
		require.Equal(t, http.StatusCreated, resp.StatusCode, "%s", body)
		resp, _ = c.do(http.MethodGet, "/v2/alice/app/manifests/multi", nil, nil)
		assert.Equal(t, "application/vnd.oci.image.index.v1+json", resp.Header.Get("Content-Type"))

		bad, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json",
			"manifests": []any{map[string]any{"digest": digestOf([]byte("nope")), "size": 1}}})
		resp, _ = c.do(http.MethodPut, "/v2/alice/app/manifests/bad", map[string]string{"Content-Type": "application/vnd.oci.image.index.v1+json"}, bad)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "indexes reference manifests of the image")
	})

	t.Run("api", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/packages/alice/container/app", "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		versions := body["versions"].([]any)
		require.Len(t, versions, 2)
		tags := map[string]any{}
		for _, v := range versions {
			tags[v.(map[string]any)["version"].(string)] = v.(map[string]any)["tags"]
		}
		assert.Equal(t, []any{"v1"}, tags[img.digest])
		assert.Equal(t, "alice", versions[0].(map[string]any)["publisher"])
		assert.Equal(t, false, body["package"].(map[string]any)["can_write"], "anonymous")
	})

	t.Run("delete_and_gc", func(t *testing.T) {
		resp, _ := srv.registry(t, bobW).do(http.MethodDelete, "/v2/alice/app/manifests/v1", nil, nil)
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		resp, _ = c.do(http.MethodDelete, "/v2/alice/app/manifests/multi", nil, nil)
		assert.Equal(t, http.StatusAccepted, resp.StatusCode, "a tag")
		resp, _ = c.do(http.MethodGet, "/v2/alice/app/manifests/multi", nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)

		app := srv.deps.PackagesApp
		assert.Zero(t, app.CollectGarbage(), "everything is referenced, and recent")
		for _, d := range []string{img.digest, digestOf(idx)} {
			resp, _ = c.do(http.MethodDelete, "/v2/alice/app/manifests/"+d, nil, nil)
			assert.Equal(t, http.StatusAccepted, resp.StatusCode, "a tag only untags: digests delete")
		}
		assert.Equal(t, http.StatusNotFound, c.pull("alice/app", "v1"))

		app.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
		defer func() { app.Now = nil }()
		assert.Positive(t, app.CollectGarbage())
		assert.Zero(t, srv.countRows(t, &domain.ContainerBlob{}, "owner_id = ?", srv.userID(t, "alice")), "unreferenced blobs go")
		assert.Zero(t, srv.countRows(t, &domain.ContainerUpload{}, "1 = 1"), "abandoned uploads go")

		// Pushed again: everything is uploaded again.
		app.Now = nil
		c.push("alice/app", "v1", img)
		assert.Equal(t, http.StatusOK, c.pull("alice/app", "v1"))
	})

	t.Run("reused_blobs_survive_gc", func(t *testing.T) {
		// A blob stored long ago and unreferenced, then used by a push that
		// found it (HEAD) instead of uploading it: GC must keep it.
		app := srv.deps.PackagesApp
		img2 := newTestImage(t, "two")
		require.Equal(t, http.StatusCreated, c.pushBlob("alice/other", img2.config, 0))
		require.Equal(t, http.StatusCreated, c.pushBlob("alice/other", img2.layer, 0))
		resp, _ := srv.registry(t, nil).do(http.MethodHead, "/v2/alice/app/blobs/"+digestOf(img2.layer), nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "a public image only serves its own blobs")
		app.Now = func() time.Time { return time.Now().Add(23 * time.Hour) }
		resp, _ = c.do(http.MethodHead, "/v2/alice/other/blobs/"+digestOf(img2.layer), nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		app.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
		app.CollectGarbage()
		app.Now = nil
		resp, _ = c.do(http.MethodHead, "/v2/alice/other/blobs/"+digestOf(img2.layer), nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "touched by the push's check")
		resp, _ = c.do(http.MethodHead, "/v2/alice/other/blobs/"+digestOf(img2.config), nil, nil)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "the untouched one went")
	})
}

// generic sends a request to the generic package routes.
func (s *testServer) generic(t *testing.T, method, path string, auth *githttp.BasicAuth, body []byte) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, s.url+"/api/packages/"+path, bytes.NewReader(body))
	require.NoError(t, err)
	if auth != nil {
		req.SetBasicAuth(auth.Username, auth.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp.Header
}

func TestGenericPackages(t *testing.T) {
	t.Run("local", func(t *testing.T) { testGenericPackages(t) })
	t.Run("s3_fake", func(t *testing.T) { testGenericPackages(t, withS3(s3test.StartFake(t))) })
}

func testGenericPackages(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, func(c *config.Config) { c.Packages.MaxFileSize = 1 << 20 })...)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	write := srv.createToken(t, alice.session, "publish", "write:packages")
	bobW := srv.createToken(t, bob.session, "publish", "write:packages")
	content := []byte("<html><script>alert(1)</script></html>")
	const file = "alice/generic/tool/1.0.0/tool.html"

	t.Run("publish", func(t *testing.T) {
		status, body, _ := srv.generic(t, http.MethodPut, file, nil, content)
		assert.Equal(t, http.StatusUnauthorized, status)
		status, body, _ = srv.generic(t, http.MethodPut, file, write, content)
		require.Equal(t, http.StatusCreated, status, "%s", body)
		sum := sha256.Sum256(content)
		assert.Contains(t, string(body), hex.EncodeToString(sum[:]))
		status, _, _ = srv.generic(t, http.MethodPut, file, write, []byte("changed"))
		assert.Equal(t, http.StatusConflict, status, "a version's file is published once")
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/tool/1.0.0/other.bin", write, []byte("more"))
		assert.Equal(t, http.StatusCreated, status, "more files in a version")
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/tool/2.0.0/big.bin", write, bytes.Repeat([]byte("x"), 1<<20+1))
		assert.Equal(t, http.StatusRequestEntityTooLarge, status, "packages.max_file_size")
		status, _, _ = srv.generic(t, http.MethodGet, "alice/generic/tool/2.0.0/big.bin", write, nil)
		assert.Equal(t, http.StatusNotFound, status, "nothing kept")
		for _, bad := range []string{"alice/generic/.tool/1/a", "alice/generic/tool/1/..%2Fx", "alice/generic/tool/-1/a"} {
			status, _, _ = srv.generic(t, http.MethodPut, bad, write, content)
			assert.Contains(t, []int{http.StatusBadRequest, http.StatusNotFound}, status, bad)
		}
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/tool/1.0.1/a", alice.git, content)
		assert.Equal(t, http.StatusForbidden, status, "write:packages needed")
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/mine/1/a", bobW, content)
		assert.Equal(t, http.StatusNotFound, status, "not in someone else's namespace")
	})

	t.Run("download", func(t *testing.T) {
		status, _, h := srv.generic(t, http.MethodGet, file, nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "private by default")
		assert.NotEmpty(t, h.Get("WWW-Authenticate"))
		status, _, _ = srv.generic(t, http.MethodGet, file, bobW, nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, data, h := srv.generic(t, http.MethodGet, file, write, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, content, data)
		assert.Equal(t, "application/octet-stream", h.Get("Content-Type"), "never rendered")
		assert.Contains(t, h.Get("Content-Disposition"), "attachment")
		assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))

		// The web UI's cookie works for downloads.
		req, _ := http.NewRequest(http.MethodGet, srv.url+"/api/packages/"+file, nil)
		req.AddCookie(&http.Cookie{Name: "ogit_session", Value: alice.session})
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		req, _ = http.NewRequest(http.MethodPut, srv.url+"/api/packages/alice/generic/tool/3/a", bytes.NewReader(content))
		req.AddCookie(&http.Cookie{Name: "ogit_session", Value: alice.session})
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "but not for uploads (no CSRF check here)")
	})

	t.Run("linked_repository", func(t *testing.T) {
		srv.createRepo(t, alice.session, "Tool", true)
		status, body := srv.request(t, http.MethodPatch, "/api/v1/packages/alice/generic/tool", bob.session, map[string]any{"repository": "alice/tool"})
		assert.Equal(t, http.StatusNotFound, status, "%v", body)
		status, body = srv.request(t, http.MethodPatch, "/api/v1/packages/alice/generic/tool", alice.session, map[string]any{"repository": "bob/nothing"})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		status, body = srv.request(t, http.MethodPatch, "/api/v1/packages/alice/generic/tool", alice.session, map[string]any{"repository": "alice/tool", "visibility": "repository"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "alice/tool", body["repository"])

		status, _ = srv.post(t, repoAPI("alice/tool")+"/members", alice.session, map[string]any{"username": "bob", "role": "reporter"})
		require.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodGet, file, bobW, nil)
		assert.Equal(t, http.StatusOK, status, "the repository's readers read it")
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/tool/1.1.0/a", bobW, content)
		assert.Equal(t, http.StatusForbidden, status, "reporters don't publish")
		status, _ = srv.request(t, http.MethodPatch, repoAPI("alice/tool")+"/members/"+srv.members(t, "alice/tool", alice)["bob"]["id"].(string), alice.session, map[string]any{"role": "developer"})
		require.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodPut, "alice/generic/tool/1.1.0/a", bobW, content)
		assert.Equal(t, http.StatusCreated, status, "writers do")
		status, _ = srv.request(t, http.MethodDelete, "/api/v1/packages/alice/generic/tool/versions/1.1.0", bob.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "developers don't manage it")

		status, body = srv.get(t, "/api/v1/packages/alice/generic/tool", alice.session)
		require.Equal(t, http.StatusOK, status)
		for _, v := range body["versions"].([]any) {
			if v.(map[string]any)["version"] == "1.0.0" {
				assert.EqualValues(t, len(content)+4, v.(map[string]any)["size"], "the files' total")
			}
		}
		status, body = srv.get(t, repoAPI("alice/tool")+"/packages", bob.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Len(t, body["packages"], 1)
		status, _ = srv.get(t, repoAPI("alice/tool")+"/packages", "")
		assert.Equal(t, http.StatusNotFound, status)

		status, _ = srv.request(t, http.MethodPatch, repoAPI("alice/tool"), alice.session, map[string]any{"is_private": false})
		require.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodGet, file, nil, nil)
		assert.Equal(t, http.StatusOK, status, "follows the repository: now public")
		status, body = srv.get(t, "/api/v1/packages/alice", "")
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["packages"], 1)
	})

	t.Run("deleting", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, "/api/v1/packages/alice/generic/tool/versions/1.1.0", alice.session, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodGet, "alice/generic/tool/1.1.0/a", write, nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = srv.generic(t, http.MethodDelete, "alice/generic/tool/1.0.0", bobW, nil)
		assert.Equal(t, http.StatusForbidden, status)

		// The repository goes: the package stays, private.
		status, _ = srv.request(t, http.MethodDelete, repoAPI("alice/tool")+"?confirm=tool", alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodGet, file, nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "private again")
		status, body := srv.get(t, "/api/v1/packages/alice/generic/tool", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "private", body["package"].(map[string]any)["visibility"])
		assert.Nil(t, body["package"].(map[string]any)["repository"])
		assert.Zero(t, srv.countRows(t, &domain.Package{}, "repository_id IS NOT NULL"), "unlinked")

		status, _ = srv.request(t, http.MethodDelete, "/api/v1/packages/alice/generic/tool?confirm=nope", alice.session, nil)
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.request(t, http.MethodDelete, "/api/v1/packages/alice/generic/tool?confirm=tool", alice.session, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _, _ = srv.generic(t, http.MethodGet, file, write, nil)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Zero(t, srv.countRows(t, &domain.PackageFile{}, "1 = 1"))
	})
}

func TestPackagesInGroups(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	carol := srv.newUser(t, "carol")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "team"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	for name, role := range map[string]string{"bob": "maintainer", "carol": "member"} {
		status, _ = srv.post(t, "/api/v1/group/team/members", alice.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status)
	}
	bobW := srv.createToken(t, bob.session, "p", "write:packages")
	carolW := srv.createToken(t, carol.session, "p", "write:packages")

	status, _, _ = srv.generic(t, http.MethodPut, "team/generic/lib/1/a", carolW, []byte("a"))
	assert.Equal(t, http.StatusNotFound, status, "members don't create packages")
	status, _, _ = srv.generic(t, http.MethodPut, "team/generic/lib/1/a", bobW, []byte("a"))
	require.Equal(t, http.StatusCreated, status, "maintainers do")
	status, _, _ = srv.generic(t, http.MethodGet, "team/generic/lib/1/a", carolW, nil)
	assert.Equal(t, http.StatusOK, status, "members read")
	status, _, _ = srv.generic(t, http.MethodPut, "team/generic/lib/2/a", carolW, []byte("a"))
	assert.Equal(t, http.StatusForbidden, status, "members publish only through a linked repository they write to")

	status, body = srv.request(t, http.MethodDelete, "/api/v1/group/team", alice.session, nil)
	assert.Equal(t, http.StatusConflict, status, "%v", body)
	assert.Equal(t, "NAMESPACE_HAS_PACKAGES", body["type"])

	// Users with packages can't be deleted either.
	srv.enableTOTP(t, alice)
	srv.promote(t, "alice")
	status, _, _ = srv.generic(t, http.MethodPut, "carol/generic/x/1/a", carolW, []byte("a"))
	require.Equal(t, http.StatusCreated, status)
	status, body = srv.request(t, http.MethodDelete, "/api/v1/admin/users/carol", alice.session, nil)
	assert.Equal(t, http.StatusConflict, status)
	assert.Contains(t, body["details"], "packages: generic/x")
}

func TestPackagesFromActions(t *testing.T) {
	f := newPullFixture(t, true)
	srv := f.srv
	runner := srv.registerRunner(t, f.owner, actionsAPI("owner/app")+"/runners/registration-token", "r1", "ubuntu-latest")
	const publish = "name: Publish\non: push\npermissions:\n  packages: write\njobs:\n  a:\n    runs-on: ubuntu-latest\n    steps: [{run: 'true'}]\n"
	pushFiles(t, f.url, f.owner.git, "main", "ci", map[string]string{".github/workflows/publish.yml": publish, ".github/workflows/ci.yml": ciWorkflow})
	tokens := map[string]string{}
	for range 2 {
		task := fetch0(t, runner, "ubuntu-latest")
		require.NotNil(t, task)
		tokens[task.WorkflowPath] = task.Token
	}
	writer := &githttp.BasicAuth{Username: "x", Password: tokens[".github/workflows/publish.yml"]}
	reader := &githttp.BasicAuth{Username: "x", Password: tokens[".github/workflows/ci.yml"]}

	status, body, _ := srv.generic(t, http.MethodPut, "owner/generic/app/1/app.tgz", reader, []byte("a"))
	assert.Equal(t, http.StatusForbidden, status, "packages: read by default: %s", body)
	status, body, _ = srv.generic(t, http.MethodPut, "owner/generic/app/1/app.tgz", writer, []byte("a"))
	require.Equal(t, http.StatusCreated, status, "%s", body)
	status, _, _ = srv.generic(t, http.MethodGet, "owner/generic/app/1/app.tgz", reader, nil)
	assert.Equal(t, http.StatusOK, status, "reads its repository's packages")
	status, _, _ = srv.generic(t, http.MethodPut, f.outer.name+"/generic/app/1/app.tgz", writer, []byte("a"))
	assert.Equal(t, http.StatusNotFound, status, "only in its repository's namespace")

	status, pkg := srv.get(t, "/api/v1/packages/owner/generic/app", f.owner.session)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "owner/app", pkg["package"].(map[string]any)["repository"], "linked to the job's repository")
	assert.Equal(t, "repository", pkg["package"].(map[string]any)["visibility"])
	status, _, _ = srv.generic(t, http.MethodGet, "owner/generic/app/1/app.tgz", f.reporter.git, nil)
	assert.Equal(t, http.StatusNotFound, status, "repository scopes don't read packages")

	// docker login with GITHUB_TOKEN.
	c := srv.registry(t, writer)
	c.push("owner/app", "latest", newTestImage(t, "ci"))
	assert.Equal(t, http.StatusOK, srv.registry(t, reader).pull("owner/app", "latest"))

	// Unlinked packages of the namespace are not the job's.
	ownerW := srv.createToken(t, f.owner.session, "p", "write:packages")
	status, _, _ = srv.generic(t, http.MethodPut, "owner/generic/private/1/a", ownerW, []byte("a"))
	require.Equal(t, http.StatusCreated, status)
	status, _, _ = srv.generic(t, http.MethodPut, "owner/generic/private/2/a", writer, []byte("a"))
	assert.Equal(t, http.StatusNotFound, status)
	status, _, _ = srv.generic(t, http.MethodGet, "owner/generic/private/1/a", writer, nil)
	assert.Equal(t, http.StatusNotFound, status)
	// Nor those of another repository.
	srv.createRepo(t, f.owner.session, "Other", true)
	status, _ = srv.request(t, http.MethodPatch, "/api/v1/packages/owner/generic/private", f.owner.session, map[string]any{"repository": "owner/other"})
	require.Equal(t, http.StatusOK, status)
	status, _, _ = srv.generic(t, http.MethodGet, "owner/generic/private/1/a", writer, nil)
	assert.Equal(t, http.StatusNotFound, status, "another repository's package")
}

func TestPackagesLargeUploads(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) { c.Packages.MaxFileSize = 8 << 20 })
	alice := srv.newUser(t, "alice")
	w := srv.createToken(t, alice.session, "p", "write:packages")
	big := bytes.Repeat([]byte("0123456789abcdef"), 6<<20/16) // over the 4 MB body limit

	status, body, _ := srv.generic(t, http.MethodPut, "alice/generic/big/1/big.bin", w, big)
	require.Equal(t, http.StatusCreated, status, "streamed: %s", body)
	status, data, _ := srv.generic(t, http.MethodGet, "alice/generic/big/1/big.bin", w, nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, digestOf(big), digestOf(data))
	basic := map[string]string{"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte("x:"+w.Password))}
	assert.True(t, srv.tooLarge(t, http.MethodPut, "/api/packages/alice/generic/big/2/big.bin", basic, bytes.NewReader(append(big, big...))))

	c := srv.registry(t, w)
	require.Equal(t, http.StatusOK, c.login(), "docker logs in first")
	assert.Equal(t, http.StatusCreated, c.pushBlob("alice/big", big, 0), "monolithic")
	assert.Equal(t, http.StatusCreated, c.pushBlob("alice/big", append([]byte("x"), big...), 2), "chunked")
	auth := map[string]string{"Authorization": "Bearer " + c.token}
	huge := append(big, big...)
	assert.True(t, srv.tooLarge(t, http.MethodPost, "/v2/alice/big/blobs/uploads/?digest="+digestOf(huge), auth, bytes.NewReader(huge)))
}
