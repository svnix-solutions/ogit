package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Module paths can't have ports: the module host is public_url's.
const goHost = "ogit.test"

func withGoHost(c *config.Config) { c.Server.PublicURL = "http://" + goHost }

func TestGoProxy(t *testing.T) {
	t.Run("local", func(t *testing.T) { testGoProxy(t) })
	t.Run("s3_fake", func(t *testing.T) { testGoProxy(t, withS3(s3test.StartFake(t))) })
}

func testGoProxy(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, append(opts, withGoHost)...)
	alice := srv.newUser(t, "alice")
	bob := srv.newUser(t, "bob")
	srv.createRepo(t, alice.session, "Lib", false)
	url := srv.url + "/alice/lib.git"
	proxy := srv.url + "/api/packages/go/" + goHost + "/alice/lib"

	gomod := "module " + goHost + "/alice/lib\n\ngo 1.21\n"
	pushFiles(t, url, alice.git, "main", "v1", map[string]string{"go.mod": gomod, "lib.go": "package lib\n\nfunc Hello() string { return \"v1\" }\n",
		"sub/go.mod": "module " + goHost + "/alice/lib/sub\n", "sub/x.go": "package sub\n", "vendor/example.com/dep/d.go": "package dep\n"})
	pushTag(t, url, alice, "main", "v1.0.0")
	pushFiles(t, url, alice.git, "main", "rc", map[string]string{"lib.go": "package lib\n\nfunc Hello() string { return \"rc\" }\n"})
	pushTag(t, url, alice, "main", "v1.1.0-rc.1")
	pushTag(t, url, alice, "main", "not-semver")
	head := pushFiles(t, url, alice.git, "main", "next", map[string]string{"lib.go": "package lib\n\nfunc Hello() string { return \"next\" }\n"})
	// v2 lives on a branch with its own module path; a v3 tag without one
	// isn't a version of anything.
	pushFiles(t, url, alice.git, "v2", "v2", map[string]string{"go.mod": "module " + goHost + "/alice/lib/v2\n", "lib.go": "package lib\n\nfunc Hello() string { return \"v2\" }\n"})
	pushTag(t, url, alice, "v2", "v2.0.0")
	pushTag(t, url, alice, "v2", "v3.0.0")

	t.Run("list_and_latest", func(t *testing.T) {
		status, _, body := fetch(t, proxy+"/@v/list", nil)
		require.Equal(t, http.StatusOK, status)
		list := strings.Fields(string(body))
		sort.Strings(list)
		assert.Equal(t, []string{"v1.0.0", "v1.1.0-rc.1"}, list)
		_, _, body = fetch(t, proxy+"/@latest", nil)
		assert.Contains(t, string(body), `"Version":"v1.0.0"`, "releases before pre-releases")
		_, _, body = fetch(t, proxy+"/v2/@v/list", nil)
		assert.Equal(t, "v2.0.0\n", string(body))
		status, _, _ = fetch(t, proxy+"/v3/@v/list", nil)
		assert.Equal(t, http.StatusOK, status)
		_, _, body = fetch(t, proxy+"/v3/@v/list", nil)
		assert.Empty(t, string(body), "v3.0.0's go.mod says /v2")
	})

	t.Run("info_mod_zip", func(t *testing.T) {
		_, _, body := fetch(t, proxy+"/@v/v1.0.0.mod", nil)
		assert.Equal(t, gomod, string(body))
		status, _, body := fetch(t, proxy+"/@v/v1.0.0.zip", nil)
		require.Equal(t, http.StatusOK, status)
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		require.NoError(t, err)
		var names []string
		for _, f := range zr.File {
			names = append(names, f.Name)
		}
		sort.Strings(names)
		prefix := goHost + "/alice/lib@v1.0.0/"
		assert.Equal(t, []string{prefix + "README.md", prefix + "go.mod", prefix + "lib.go"}, names, "no nested module, no vendored packages")

		var info struct{ Version, Time string }
		_, _, body = fetch(t, proxy+"/@v/main.info", nil)
		require.NoError(t, json.Unmarshal(body, &info))
		assert.True(t, strings.HasPrefix(info.Version, "v1.1.0-rc.1.0."), "a pseudo-version after the highest ancestor tag: %s", info.Version)
		assert.True(t, strings.HasSuffix(info.Version, head[:12]))
		status, _, body = fetch(t, proxy+"/@v/"+info.Version+".mod", nil)
		assert.Equal(t, http.StatusOK, status, "the pseudo-version resolves back")
		assert.Equal(t, gomod, string(body))
		status, _, _ = fetch(t, proxy+"/@v/"+head[:10]+".info", nil)
		assert.Equal(t, http.StatusOK, status, "a short hash")
		status, _, _ = fetch(t, proxy+"/@v/v1.1.0-rc.1.0.20000101000000-"+head[:12]+".mod", nil)
		assert.Equal(t, http.StatusNotFound, status, "the time must match the commit")
		status, _, _ = fetch(t, proxy+"/@v/v9.9.9.info", nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = fetch(t, srv.url+"/api/packages/go/other.host/alice/lib/@v/list", nil)
		assert.Equal(t, http.StatusNotFound, status, "another host's module")
	})

	t.Run("api", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("alice/lib")+"/go-module", "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, goHost+"/alice/lib", body["path"])
		assert.Len(t, body["versions"], 2)
		assert.Equal(t, []any{}, body["requires"])
		srv.createRepo(t, alice.session, "Plain", false)
		status, body = srv.get(t, repoAPI("alice/plain")+"/go-module", "")
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "GO_MODULE_NOT_FOUND", body["type"])
	})

	t.Run("private", func(t *testing.T) {
		srv.createRepo(t, alice.session, "Secret", true)
		surl := srv.url + "/alice/secret.git"
		pushFiles(t, surl, alice.git, "main", "v1", map[string]string{"go.mod": "module " + goHost + "/alice/secret\n", "s.go": "package secret\n"})
		pushTag(t, surl, alice, "main", "v1.0.0")
		sproxy := srv.url + "/api/packages/go/" + goHost + "/alice/secret"
		status, _, _ := fetch(t, sproxy+"/@v/list", nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _, _ = fetch(t, sproxy+"/@v/list", basicAuth(bob.git.Username, bob.git.Password))
		assert.Equal(t, http.StatusNotFound, status)
		status, _, body := fetch(t, sproxy+"/@v/list", basicAuth(alice.git.Username, alice.git.Password))
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "v1.0.0\n", string(body))
	})

	t.Run("go_get_meta", func(t *testing.T) {
		for _, path := range []string{"/alice/lib?go-get=1", "/alice/lib/v2/sub?go-get=1", "/nobody/nothing?go-get=1"} {
			status, _, body := fetch(t, srv.url+path, nil)
			assert.Equal(t, http.StatusOK, status, path)
			assert.Contains(t, string(body), `<meta name="go-import" content="`+goHost+"/"+strings.Join(strings.Split(strings.Split(path, "?")[0], "/")[1:3], "/")+` mod http://`+goHost+`/api/packages/go">`, path)
		}
		status, _, body := fetch(t, srv.url+"/alice/lib", nil)
		assert.Equal(t, http.StatusOK, status)
		assert.NotContains(t, string(body), "go-import", "only with ?go-get=1")
	})

	t.Run("go_command", func(t *testing.T) {
		goBin, err := exec.LookPath("go")
		if err != nil {
			t.Skip("no go command")
		}
		dir, home := t.TempDir(), t.TempDir()
		// go sends credentials (.netrc) over HTTPS only, so the private
		// module's auth is checked above, without it.
		goproxy := srv.url + "/api/packages/go"
		run := func(args ...string) string {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
			defer cancel()
			cmd := exec.CommandContext(ctx, goBin, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "HOME="+home, "GOPATH="+filepath.Join(home, "go"), "GOMODCACHE="+filepath.Join(home, "mod"),
				"GOFLAGS=-modcacherw", "GOPROXY="+goproxy, "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GO111MODULE=on")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "go %s:\n%s", strings.Join(args, " "), out)
			return string(out)
		}
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/app\n\ngo 1.21\n"), 0o644))
		run("mod", "download", "-json", goHost+"/alice/lib@v1.0.0")
		run("get", goHost+"/alice/lib@main")
		gm, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		require.NoError(t, err)
		assert.Contains(t, string(gm), goHost+"/alice/lib v1.1.0-rc.1.0.")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte(`package main

import (
	"fmt"

	"`+goHost+`/alice/lib"
)

func main() { fmt.Print(lib.Hello()) }
`), 0o644))
		run("get", goHost+"/alice/lib@v1.0.0")
		assert.Equal(t, "v1", run("run", "."))
	})
}

func basicAuth(user, pass string) map[string]string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	return map[string]string{"Authorization": req.Header.Get("Authorization")}
}

func TestGoProxySubgroups(t *testing.T) {
	srv := startTestServer(t, withGoHost)
	alice := srv.newUser(t, "alice")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.subgroup(t, alice, "org", "team")
	require.Equal(t, http.StatusOK, status, "%v", body)
	for name, private := range map[string]bool{"Lib": false, "Secret": true} {
		status, body = srv.createIn(t, alice, "org/team", name, private)
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	url := srv.url + "/org/team/lib.git"
	pushFiles(t, url, alice.git, "main", "v1", map[string]string{"go.mod": "module " + goHost + "/org/team/lib\n", "lib.go": "package lib\n"})
	pushTag(t, url, alice, "main", "v1.0.0")

	status, _, data := fetch(t, srv.url+"/api/packages/go/"+goHost+"/org/team/lib/@v/list", nil)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "v1.0.0\n", string(data))
	status, _, _ = fetch(t, srv.url+"/api/packages/go/"+goHost+"/org/team/lib/@v/v1.0.0.zip", nil)
	assert.Equal(t, http.StatusOK, status)

	meta := func(path string) string {
		_, _, data := fetch(t, srv.url+path+"?go-get=1", nil)
		return string(data)
	}
	assert.Contains(t, meta("/org/team/lib/sub/pkg"), `content="`+goHost+`/org/team/lib mod `, "the repository is the module root")
	assert.Contains(t, meta("/org/team/secret/pkg"), `content="`+goHost+`/org/team mod `, "a private repository: the answer says nothing about it")
}
