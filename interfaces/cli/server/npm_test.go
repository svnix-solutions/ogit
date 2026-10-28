package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// npmTarball builds a package tarball like npm pack.
func npmTarball(t *testing.T, name, version, readme string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"package/package.json": `{"name":"` + name + `","version":"` + version + `","main":"index.js"}`,
		"package/index.js":     "module.exports = " + `"` + version + `";` + "\n",
	}
	if readme != "" {
		files["package/README.md"] = readme
	}
	for n, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func integrityOf(b []byte) string {
	sum := sha512.Sum512(b)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

// npmPublishBody is what `npm publish` sends.
func npmPublishBody(name, version string, tarball []byte, distTags map[string]string, extra map[string]any) map[string]any {
	short := name[strings.Index(name, "/")+1:]
	manifest := map[string]any{"name": name, "version": version, "description": "the " + short + " package",
		"dependencies": map[string]string{"left-pad": "^1.3.0"}, "dist": map[string]any{"integrity": integrityOf(tarball)}}
	for k, v := range extra {
		manifest[k] = v
	}
	if distTags == nil {
		distTags = map[string]string{"latest": version}
	}
	return map[string]any{
		"_id": name, "name": name, "dist-tags": distTags,
		"versions":     map[string]any{version: manifest},
		"_attachments": map[string]any{name + "-" + version + ".tgz": map[string]any{"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tarball), "length": len(tarball)}},
	}
}

// npmDo sends a request to the npm registry with a token.
func (s *testServer) npmDo(t *testing.T, method, path string, token *githttp.BasicAuth, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.url+"/api/packages/npm/"+path, r)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if token != nil {
		req.Header.Set("Authorization", "Bearer "+token.Password)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestNpmRegistry(t *testing.T) {
	t.Run("local", func(t *testing.T) { testNpmRegistry(t) })
	t.Run("s3_fake", func(t *testing.T) { testNpmRegistry(t, withS3(s3test.StartFake(t))) })
}

func testNpmRegistry(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	aliceW := srv.createToken(t, alice.session, "npm", "write:packages")
	aliceR := srv.createToken(t, alice.session, "npm-read", "read:packages")
	bobW := srv.createToken(t, bob.session, "npm", "write:packages")
	tgz := npmTarball(t, "@alice/util", "1.0.0", "# Util\n\nHello.\n")
	const pkg = "@alice%2futil"

	t.Run("publish", func(t *testing.T) {
		status, body := srv.npmDo(t, http.MethodPut, pkg, nil, npmPublishBody("@alice/util", "1.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
		status, _ = srv.npmDo(t, http.MethodPut, pkg, bobW, npmPublishBody("@alice/util", "1.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusNotFound, status, "not bob's scope")
		status, _ = srv.npmDo(t, http.MethodPut, "util", aliceW, npmPublishBody("util", "1.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusBadRequest, status, "scoped names only")
		bad := npmPublishBody("@alice/util", "1.0.0", tgz, nil, map[string]any{"dist": map[string]any{"integrity": integrityOf([]byte("other"))}})
		status, body = srv.npmDo(t, http.MethodPut, pkg, aliceW, bad)
		assert.Equal(t, http.StatusBadRequest, status, "integrity: %v", body)
		status, _ = srv.npmDo(t, http.MethodPut, pkg, aliceR, npmPublishBody("@alice/util", "1.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusForbidden, status, "read:packages doesn't publish")

		status, body = srv.npmDo(t, http.MethodPut, pkg, aliceW, npmPublishBody("@alice/util", "1.0.0", tgz, nil, nil))
		require.Equal(t, http.StatusCreated, status, "%v", body)
		status, body = srv.npmDo(t, http.MethodPut, "@alice/util", aliceW, npmPublishBody("@alice/util", "1.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusForbidden, status, "versions are immutable: %v", body)
		tgz2 := npmTarball(t, "@alice/util", "2.0.0-beta.1", "")
		status, _ = srv.npmDo(t, http.MethodPut, pkg, aliceW, npmPublishBody("@alice/util", "2.0.0-beta.1", tgz2, map[string]string{"next": "2.0.0-beta.1"}, nil))
		require.Equal(t, http.StatusCreated, status)
	})

	t.Run("install", func(t *testing.T) {
		status, doc := srv.npmDo(t, http.MethodGet, pkg, aliceR, nil)
		require.Equal(t, http.StatusOK, status, "%v", doc)
		assert.Equal(t, map[string]any{"latest": "1.0.0", "next": "2.0.0-beta.1"}, doc["dist-tags"])
		assert.Equal(t, "# Util\n\nHello.\n", doc["readme"], "from the tarball")
		assert.Equal(t, "the util package", doc["description"])
		v := doc["versions"].(map[string]any)["1.0.0"].(map[string]any)
		dist := v["dist"].(map[string]any)
		assert.Equal(t, integrityOf(tgz), dist["integrity"])
		assert.Equal(t, srv.url+"/api/packages/npm/@alice/util/-/util-1.0.0.tgz", dist["tarball"])
		assert.Equal(t, map[string]any{"left-pad": "^1.3.0"}, v["dependencies"])

		status, _, data := fetch(t, dist["tarball"].(string), map[string]string{"Authorization": "Bearer " + aliceR.Password})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, tgz, data)
		req, _ := http.NewRequest(http.MethodGet, dist["tarball"].(string), nil)
		req.AddCookie(&http.Cookie{Name: "ogit_session", Value: alice.session})
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "the web UI's download link")
		status, _ = srv.npmDo(t, http.MethodGet, pkg, nil, nil)
		assert.Equal(t, http.StatusNotFound, status, "private by default")
		status, _ = srv.npmDo(t, http.MethodGet, pkg, bobW, nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = fetch(t, dist["tarball"].(string), nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = fetch(t, srv.url+"/api/packages/npm/@alice/util/-/other-1.0.0.tgz", map[string]string{"Authorization": "Bearer " + aliceR.Password})
		assert.Equal(t, http.StatusNotFound, status)

		status, api := srv.get(t, "/api/v1/packages/alice/npm/util", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "# Util\n\nHello.\n", api["readme"], "the latest version's README")
		for _, v := range api["versions"].([]any) {
			meta := v.(map[string]any)["metadata"].(map[string]any)
			assert.NotContains(t, meta, "readme", "not repeated per version")
			if v.(map[string]any)["version"] == "1.0.0" {
				assert.Equal(t, map[string]any{"left-pad": "^1.3.0"}, meta["manifest"].(map[string]any)["dependencies"])
				assert.ElementsMatch(t, []any{"latest"}, v.(map[string]any)["tags"])
			}
		}
	})

	t.Run("dist_tags", func(t *testing.T) {
		status, tags := srv.npmDo(t, http.MethodGet, "-/package/"+pkg+"/dist-tags", aliceR, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[string]any{"latest": "1.0.0", "next": "2.0.0-beta.1"}, tags)
		status, _ = srv.npmDo(t, http.MethodPut, "-/package/"+pkg+"/dist-tags/stable", aliceR, "1.0.0")
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.npmDo(t, http.MethodPut, "-/package/"+pkg+"/dist-tags/stable", aliceW, "1.0.0")
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.npmDo(t, http.MethodPut, "-/package/"+pkg+"/dist-tags/stable", aliceW, "9.9.9")
		assert.Equal(t, http.StatusNotFound, status, "an unknown version")
		status, _ = srv.npmDo(t, http.MethodPut, "-/package/"+pkg+"/dist-tags/1.2.3", aliceW, "1.0.0")
		assert.Equal(t, http.StatusBadRequest, status, "a tag can't look like a version")
		status, _ = srv.npmDo(t, http.MethodDelete, "-/package/"+pkg+"/dist-tags/latest", aliceW, nil)
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.npmDo(t, http.MethodDelete, "-/package/"+pkg+"/dist-tags/stable", aliceW, nil)
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("deprecate_and_unpublish", func(t *testing.T) {
		_, doc := srv.npmDo(t, http.MethodGet, pkg, aliceW, nil)
		versions := doc["versions"].(map[string]any)
		versions["1.0.0"].(map[string]any)["deprecated"] = "use 2"
		srv.createRepo(t, alice.session, "Util", true)
		status, body := srv.request(t, http.MethodPatch, "/api/v1/packages/alice/npm/util", alice.session, map[string]any{"repository": "alice/util"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, repoAPI("alice/util")+"/members", alice.session, map[string]any{"username": "bob", "role": "developer"})
		require.Equal(t, http.StatusOK, status)

		status, _ = srv.npmDo(t, http.MethodPut, pkg+"/-rev/1-x", aliceR, doc)
		assert.Equal(t, http.StatusForbidden, status, "read:packages doesn't deprecate")
		status, _ = srv.npmDo(t, http.MethodPut, pkg+"/-rev/1-x", bobW, doc)
		assert.Equal(t, http.StatusOK, status, "writers deprecate")
		_, after := srv.npmDo(t, http.MethodGet, pkg, aliceW, nil)
		assert.Equal(t, "use 2", after["versions"].(map[string]any)["1.0.0"].(map[string]any)["deprecated"])

		delete(versions, "1.0.0")
		status, _ = srv.npmDo(t, http.MethodPut, pkg+"/-rev/1-x", bobW, doc)
		assert.Equal(t, http.StatusForbidden, status, "unpublishing needs a package admin")
		status, _ = srv.npmDo(t, http.MethodPut, pkg+"/-rev/1-x", aliceW, doc)
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.npmDo(t, http.MethodDelete, "@alice/util/-/util-1.0.0.tgz/-rev/1-x", aliceW, nil)
		assert.Equal(t, http.StatusOK, status)
		_, after = srv.npmDo(t, http.MethodGet, pkg, aliceW, nil)
		assert.NotContains(t, after["versions"], "1.0.0")
		assert.Equal(t, "2.0.0-beta.1", after["dist-tags"].(map[string]any)["latest"], "latest follows what's left")
	})

	t.Run("search_and_whoami", func(t *testing.T) {
		tgz := npmTarball(t, "@bob/tool", "0.1.0", "")
		status, _ := srv.npmDo(t, http.MethodPut, "@bob%2ftool", bobW, npmPublishBody("@bob/tool", "0.1.0", tgz, nil, map[string]any{"keywords": []string{"cli"}}))
		require.Equal(t, http.StatusCreated, status)
		search := func(token *githttp.BasicAuth, text string) []string {
			status, body := srv.npmDo(t, http.MethodGet, "-/v1/search?text="+url.QueryEscape(text), token, nil)
			require.Equal(t, http.StatusOK, status)
			var names []string
			for _, o := range body["objects"].([]any) {
				names = append(names, o.(map[string]any)["package"].(map[string]any)["name"].(string))
			}
			return names
		}
		assert.Equal(t, []string{"@bob/tool"}, search(bobW, "tool"))
		assert.Empty(t, search(nil, "tool"), "private")
		assert.ElementsMatch(t, []string{"@bob/tool", "@alice/util"}, search(bobW, ""), "bob reads alice's through the repository")
		assert.Equal(t, []string{"@alice/util"}, search(aliceR, "@alice"))

		status, body := srv.npmDo(t, http.MethodGet, "-/whoami", aliceR, nil)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "alice", body["username"])
		status, _ = srv.npmDo(t, http.MethodGet, "-/whoami", nil, nil)
		assert.Equal(t, http.StatusUnauthorized, status)
		status, _ = srv.npmDo(t, http.MethodPut, "-/user/org.couchdb.user:alice", nil, map[string]any{"name": "alice", "password": "x"})
		assert.Equal(t, http.StatusUnauthorized, status, "npm login: tokens only")
	})

	t.Run("delete_package", func(t *testing.T) {
		status, _ := srv.npmDo(t, http.MethodDelete, pkg+"/-rev/1-x", bobW, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.npmDo(t, http.MethodDelete, pkg+"/-rev/1-x", aliceW, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.npmDo(t, http.MethodGet, pkg, aliceW, nil)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("npm_cli", func(t *testing.T) {
		npmBin, err := exec.LookPath("npm")
		if err != nil {
			t.Skip("no npm")
		}
		home := t.TempDir()
		registry := srv.url + "/api/packages/npm/"
		rc := filepath.Join(home, ".npmrc")
		hostPath := strings.TrimPrefix(registry, "http:")
		require.NoError(t, os.WriteFile(rc, []byte("@alice:registry="+registry+"\n"+hostPath+":_authToken="+aliceW.Password+"\n"), 0o600))
		run := func(dir string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, npmBin, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "HOME="+home, "NPM_CONFIG_USERCONFIG="+rc, "NPM_CONFIG_CACHE="+filepath.Join(home, "cache"),
				"NPM_CONFIG_AUDIT=false", "NPM_CONFIG_FUND=false", "NPM_CONFIG_UPDATE_NOTIFIER=false", "NPM_CONFIG_FETCH_RETRIES=0", "NPM_CONFIG_REGISTRY=http://127.0.0.1:9/")
			out, err := cmd.CombinedOutput()
			return string(out), err
		}
		must := func(dir string, args ...string) string {
			t.Helper()
			out, err := run(dir, args...)
			require.NoError(t, err, "npm %s:\n%s", strings.Join(args, " "), out)
			return out
		}

		pkgDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@alice/cli","version":"1.0.0","main":"index.js","description":"from the CLI"}`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "index.js"), []byte(`module.exports = "cli 1";`), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "README.md"), []byte("# CLI\n"), 0o644))
		must(pkgDir, "publish")
		out, err := run(pkgDir, "publish")
		assert.Error(t, err, "twice: %s", out)
		assert.Contains(t, must(pkgDir, "whoami", "--registry", registry), "alice")

		appDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "package.json"), []byte(`{"name":"app","version":"1.0.0","private":true}`), 0o644))
		must(appDir, "install", "@alice/cli@1.0.0")
		data, err := os.ReadFile(filepath.Join(appDir, "node_modules", "@alice", "cli", "index.js"))
		require.NoError(t, err)
		assert.Equal(t, `module.exports = "cli 1";`, string(data))

		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"@alice/cli","version":"1.1.0","main":"index.js"}`), 0o644))
		must(pkgDir, "publish", "--tag", "beta")
		must(pkgDir, "dist-tag", "add", "@alice/cli@1.1.0", "stable")
		assert.Contains(t, must(pkgDir, "dist-tag", "ls", "@alice/cli"), "stable: 1.1.0")
		must(pkgDir, "deprecate", "@alice/cli@1.0.0", "use 1.1")
		assert.Contains(t, must(pkgDir, "view", "@alice/cli@1.0.0", "deprecated"), "use 1.1")
		must(pkgDir, "unpublish", "@alice/cli@1.1.0")
		out = must(pkgDir, "view", "@alice/cli", "versions", "--json")
		assert.Contains(t, out, "1.0.0")
		assert.NotContains(t, out, "1.1.0")
		assert.Contains(t, must(pkgDir, "search", "--searchlimit=5", "--registry", registry, "@alice"), "@alice/cli")
	})
}
