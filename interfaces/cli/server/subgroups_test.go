package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func groupAPI(path string) string { return "/api/v1/group/" + url.PathEscape(path) }

// subgroup creates parent/name as as.
func (s *testServer) subgroup(t *testing.T, as user, parent, name string) (int, map[string]any) {
	t.Helper()
	return s.post(t, "/api/v1/group", as.session, map[string]any{"name": name, "parent": parent})
}

func TestSubgroups(t *testing.T) {
	t.Run("local", func(t *testing.T) { testSubgroups(t) })
	t.Run("s3_fake", func(t *testing.T) { testSubgroups(t, withS3(s3test.StartFake(t))) })
}

func testSubgroups(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)
	alice, bob, carol, dave, erin := srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "carol"), srv.newUser(t, "dave"), srv.newUser(t, "erin")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	for name, role := range map[string]string{"carol": "member", "dave": "maintainer"} {
		status, body = srv.post(t, groupAPI("org")+"/members", alice.session, map[string]any{"username": name, "role": role})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}

	t.Run("create", func(t *testing.T) {
		status, body := srv.subgroup(t, alice, "org", "team")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/team", body["name"])
		assert.Equal(t, "org", body["parent"])
		status, _ = srv.subgroup(t, carol, "org", "other")
		assert.Equal(t, http.StatusForbidden, status, "members don't create sub-groups")
		status, _ = srv.subgroup(t, bob, "org", "other")
		assert.Equal(t, http.StatusNotFound, status, "outsiders don't see the group")
		status, body = srv.subgroup(t, dave, "org/team", "ops")
		assert.Equal(t, http.StatusOK, status, "an inherited maintainer creates: %v", body)
		status, _ = srv.subgroup(t, alice, "org", "team")
		assert.Equal(t, http.StatusConflict, status)
		status, _ = srv.subgroup(t, alice, "org", "Bad Name")
		assert.Equal(t, http.StatusBadRequest, status)

		parent := "org/team"
		for _, name := range []string{"b", "c"} {
			status, body = srv.subgroup(t, alice, parent, name)
			require.Equal(t, http.StatusOK, status, "%v", body)
			parent += "/" + name
		}
		assert.Equal(t, "org/team/b/c", parent)
		status, _ = srv.subgroup(t, alice, parent, "d")
		assert.Equal(t, http.StatusOK, status, "5 levels")
		status, _ = srv.subgroup(t, alice, parent+"/d", "e")
		assert.Equal(t, http.StatusBadRequest, status, "not 6")

		status, body = srv.get(t, groupAPI("org")+"/subgroups", carol.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Len(t, body["groups"], 1)
		status, body = srv.get(t, groupAPI("org/team"), carol.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "member", body["role"], "inherited")
	})

	t.Run("members", func(t *testing.T) {
		status, body := srv.post(t, groupAPI("org/team")+"/members", alice.session, map[string]any{"username": "carol", "role": "member"})
		assert.Equal(t, http.StatusConflict, status, "%v", body)
		assert.Equal(t, "INHERITED_ROLE", body["type"])
		status, _ = srv.post(t, groupAPI("org/team")+"/members", alice.session, map[string]any{"username": "carol", "role": "maintainer"})
		assert.Equal(t, http.StatusOK, status, "raising is fine")
		status, _ = srv.post(t, groupAPI("org/team")+"/members", alice.session, map[string]any{"username": "erin", "role": "member"})
		require.Equal(t, http.StatusOK, status)

		status, body = srv.get(t, groupAPI("org/team")+"/members", erin.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		got := map[string]string{}
		for _, m := range body["members"].([]any) {
			m := m.(map[string]any)
			from, _ := m["inherited_from"].(string)
			got[m["username"].(string)] = m["role"].(string) + "@" + from
		}
		assert.Equal(t, map[string]string{"alice": "owner@", "carol": "maintainer@", "erin": "member@", "dave": "maintainer@org"}, got)

		status, _ = srv.get(t, groupAPI("org"), erin.session)
		assert.Equal(t, http.StatusNotFound, status, "sub-group members aren't members of the parent")
		status, _ = srv.request(t, http.MethodDelete, groupAPI("org/team")+"/members/carol", alice.session, nil)
		assert.Equal(t, http.StatusOK, status)
	})

	var head string
	t.Run("repositories", func(t *testing.T) {
		status, body := srv.createIn(t, carol, "org/team", "App", true)
		assert.Equal(t, http.StatusForbidden, status, "an inherited member can't create: %v", body)
	})
	t.Run("repositories_by_maintainers", func(t *testing.T) {
		status, body := srv.createIn(t, dave, "org/team", "App", true)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/team/app", body["full_path"])
		status, _ = srv.subgroup(t, alice, "org/team", "app")
		assert.Equal(t, http.StatusConflict, status, "the repository's path")
		status, _ = srv.createIn(t, alice, "org", "team", true)
		assert.Equal(t, http.StatusConflict, status, "the sub-group's path")

		url := srv.url + "/org/team/app.git"
		head = pushFiles(t, url, carol.git, "main", "nested", map[string]string{"a.txt": "nested\n"})
		_, err := clone(t, url, erin.git)
		assert.NoError(t, err, "a sub-group member reads")
		_, err = clone(t, url, bob.git)
		assert.Error(t, err, "an outsider doesn't")

		status, body = srv.get(t, repoAPI("org/team/app"), carol.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "developer", body["role"], "inherited from org")
		status, _ = srv.get(t, repoAPI("org/team/app"), erin.session)
		assert.Equal(t, http.StatusOK, status)
	})
	t.Run("routes", func(t *testing.T) {
		auth := basicAuth(carol.git.Username, carol.git.Password)
		status, _, data := fetch(t, srv.url+"/org/team/app/raw/main/a.txt", auth)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "nested\n", string(data))
		status, _, _ = fetch(t, srv.url+"/org/team/app/archive/main.zip", auth)
		assert.Equal(t, http.StatusOK, status)
		status, _, _ = fetch(t, srv.url+"/org/team/app/raw/main/a.txt", nil)
		assert.Equal(t, http.StatusNotFound, status, "private")

		status, resp, _ := srv.do(t, http.MethodPost, "/org/team/app.git/info/lfs/objects/batch", map[string]string{
			"Authorization": auth["Authorization"], "Accept": "application/vnd.git-lfs+json", "Content-Type": "application/vnd.git-lfs+json"},
			map[string]any{"operation": "download", "objects": []any{map[string]any{"oid": oidOf([]byte("x")), "size": 1}}})
		assert.Equal(t, http.StatusOK, status, "LFS: %v", resp)

		var posted map[string]any
		status = srv.gh(t, http.MethodPost, "/api/v3/repos/org/team/app/statuses/"+head, map[string]string{"Authorization": "token " + carol.git.Password, "Content-Type": "application/json"},
			map[string]any{"state": "success", "context": "ci"}, &posted)
		assert.Equal(t, http.StatusCreated, status, "GitHub's API with a nested owner: %v", posted)
		var combined map[string]any
		status = srv.gh(t, http.MethodGet, "/api/v3/repos/org/team/app/commits/main/status", map[string]string{"Authorization": "token " + carol.git.Password}, nil, &combined)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "success", combined["state"])
		status, _, _ = fetch(t, srv.url+"/api/v3/repos/org/team/app/tarball/main", auth)
		assert.Equal(t, http.StatusOK, status)

		// A two-segment URL whose file path has a marker word still works.
		srv.createRepo(t, alice.session, "Plain", false)
		pushFiles(t, srv.url+"/alice/plain.git", alice.git, "main", "x", map[string]string{"raw/info/x.txt": "x\n"})
		status, _, data = fetch(t, srv.url+"/alice/plain/raw/main/raw/info/x.txt", nil)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "x\n", string(data))
	})

	t.Run("ssh", func(t *testing.T) {
		port := srv.startSSH(t)
		key := newSSHKey(t, "carol@laptop")
		status, body := srv.post(t, "/api/v1/user/ssh-keys", carol.session, map[string]any{"key": key.authorized})
		require.Equal(t, http.StatusOK, status, "%v", body)
		g := newGitCLI(t, key, port)
		dir := t.TempDir()
		g.mustRun(dir, "clone", g.url("org/team/app"), ".")
		data, err := os.ReadFile(filepath.Join(dir, "a.txt"))
		require.NoError(t, err)
		assert.Equal(t, "nested\n", string(data))
	})

	t.Run("groups_granted_on_repositories", func(t *testing.T) {
		srv.createRepo(t, bob.session, "Shared", true)
		status, _ := srv.post(t, groupAPI("org/team")+"/members", alice.session, map[string]any{"username": "bob", "role": "member"})
		require.Equal(t, http.StatusOK, status)
		status, body := srv.post(t, repoAPI("bob/shared")+"/members", bob.session, map[string]any{"group": "org/team", "role": "reporter"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		_, err := clone(t, srv.url+"/bob/shared.git", carol.git)
		assert.NoError(t, err, "org's members are members of org/team")
	})

	t.Run("two_factor", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, groupAPI("org"), alice.session, map[string]any{"require_two_factor": true})
		require.Equal(t, http.StatusOK, status)
		_, err := clone(t, srv.url+"/org/team/app.git", carol.git)
		assert.Error(t, err, "carol's membership is org's, which requires 2FA")
		_, err = clone(t, srv.url+"/org/team/app.git", erin.git)
		assert.NoError(t, err, "erin's is org/team's")
		status, _ = srv.request(t, http.MethodPatch, groupAPI("org"), alice.session, map[string]any{"require_two_factor": false})
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("namespace_view", func(t *testing.T) {
		status, body := srv.subgroup(t, alice, "org", "hidden")
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.get(t, "/api/v1/namespace/org", carol.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Len(t, body["subgroups"], 2, "members see them all")
		status, _ = srv.get(t, "/api/v1/namespace/org", "")
		assert.Equal(t, http.StatusNotFound, status, "nothing public below")
		status, body = srv.createIn(t, dave, "org/team/b", "Docs", false)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.get(t, "/api/v1/namespace/org", "")
		require.Equal(t, http.StatusOK, status, "a public repository below: %v", body)
		assert.Len(t, body["subgroups"], 1, "not org/hidden")
		status, body = srv.get(t, "/api/v1/namespace/"+url.PathEscape("org/team"), "")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Empty(t, body["repositories"], "app is private")
		_, _, data := fetch(t, srv.url+"/org/team/b/docs/raw/main/README.md", nil)
		assert.NotEmpty(t, data)
	})

	t.Run("delete", func(t *testing.T) {
		status, body := srv.request(t, http.MethodDelete, groupAPI("org/team/b/c/d"), alice.session, nil)
		assert.Equal(t, http.StatusOK, status, "an inherited owner deletes: %v", body)
		status, _ = srv.request(t, http.MethodDelete, groupAPI("org/hidden"), alice.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, body = srv.request(t, http.MethodDelete, groupAPI("org"), alice.session, nil)
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "GROUP_HAS_SUBGROUPS", body["type"])
	})
}
