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
		assert.Error(t, err, "erin's is org/team's, and org's requirement applies below it")
		status, body := srv.get(t, groupAPI("org/team"), erin.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, true, body["require_two_factor_inherited"])
		status, _ = srv.request(t, http.MethodPatch, groupAPI("org"), alice.session, map[string]any{"require_two_factor": false})
		require.Equal(t, http.StatusOK, status)

		// A sub-group's requirement doesn't reach its parent.
		status, _ = srv.request(t, http.MethodPatch, groupAPI("org/team/b"), alice.session, map[string]any{"require_two_factor": true})
		require.Equal(t, http.StatusOK, status)
		_, err = clone(t, srv.url+"/org/team/app.git", erin.git)
		assert.NoError(t, err)
		status, _ = srv.request(t, http.MethodPatch, groupAPI("org/team/b"), alice.session, map[string]any{"require_two_factor": false})
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

	t.Run("resolve", func(t *testing.T) {
		resolve := func(as string, path string) (int, map[string]any) {
			return srv.get(t, "/api/v1/resolve?path="+url.QueryEscape(path), as)
		}
		status, body := resolve(carol.session, "org/team/app/tree/main")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, map[string]any{"type": "repository", "path": "org/team/app"}, body)
		status, body = resolve(carol.session, "org/team/b")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, map[string]any{"type": "group", "path": "org/team/b"}, body)
		status, _ = resolve("", "org/team/app")
		assert.Equal(t, http.StatusNotFound, status, "private repository")
		frank := srv.newUser(t, "frank")
		status, _ = resolve(frank.session, "org/team/app/tree/main")
		assert.Equal(t, http.StatusNotFound, status, "an outsider")
		status, body = resolve("", "org/team/b")
		assert.Equal(t, http.StatusOK, status, "a public repository below: %v", body)
		status, _ = resolve("", "org/team/nothing/x")
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = resolve(alice.session, "alice/plain/tree/main")
		assert.Equal(t, http.StatusNotFound, status, "user namespaces take the usual routes")
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

func (s *testServer) moveGroup(t *testing.T, as user, group string, body map[string]any) (int, map[string]any) {
	t.Helper()
	return s.post(t, groupAPI(group)+"/move", as.session, body)
}

func TestMoveGroups(t *testing.T) {
	srv := startTestServer(t)
	alice, dave, bob := srv.newUser(t, "alice"), srv.newUser(t, "dave"), srv.newUser(t, "bob")
	for _, name := range []string{"org", "corp"} {
		status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": name})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	status, _ := srv.post(t, groupAPI("org")+"/members", alice.session, map[string]any{"username": "dave", "role": "maintainer"})
	require.Equal(t, http.StatusOK, status)
	for _, g := range [][2]string{{"org", "team"}, {"org/team", "ops"}} {
		status, body := srv.subgroup(t, alice, g[0], g[1])
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	status, body := srv.createIn(t, alice, "org/team", "App", true)
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.createIn(t, alice, "org/team/ops", "Tool", true)
	require.Equal(t, http.StatusOK, status, "%v", body)
	pushFiles(t, srv.url+"/org/team/app.git", alice.git, "main", "x", map[string]string{"a.txt": "a\n"})

	t.Run("rename", func(t *testing.T) {
		status, body := srv.moveGroup(t, dave, "org/team", map[string]any{"name": "squad"})
		assert.Equal(t, http.StatusForbidden, status, "an inherited maintainer isn't an owner: %v", body)
		status, body = srv.moveGroup(t, alice, "org/team", map[string]any{"name": "squad"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/squad", body["name"])

		status, body = srv.get(t, repoAPI("org/squad/app"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/squad/app", body["full_path"])
		status, body = srv.get(t, repoAPI("org/squad/ops/tool"), alice.session)
		assert.Equal(t, http.StatusOK, status, "sub-groups move along: %v", body)

		_, err := clone(t, srv.url+"/org/team/app.git", alice.git)
		assert.NoError(t, err, "the old git URL redirects")
		status, body = srv.get(t, repoAPI("org/team/ops/tool"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/squad/ops/tool", body["full_path"])
		status, body = srv.get(t, groupAPI("org/team"), alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "org/squad", body["name"], "the old group path too")
		status, body = srv.get(t, "/api/v1/namespace/"+url.PathEscape("org/team"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "org/squad", body["name"], "and the namespace page")
		status, body = srv.get(t, "/api/v1/resolve?path="+url.QueryEscape("org/team/app/tree/main"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, map[string]any{"type": "repository", "path": "org/squad/app", "from": "org/team/app"}, body, "the UI redirects")
		status, body = srv.get(t, "/api/v1/resolve?path="+url.QueryEscape("org/team/ops"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, map[string]any{"type": "group", "path": "org/squad/ops", "from": "org/team/ops"}, body)
		status, _, data := fetch(t, srv.url+"/org/team/app/raw/main/a.txt", basicAuth(alice.git.Username, alice.git.Password))
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "a\n", string(data))
	})

	t.Run("refused", func(t *testing.T) {
		status, body := srv.moveGroup(t, alice, "org/squad", map[string]any{"parent": "org/squad/ops"})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		assert.Equal(t, "MOVE_INTO_ITSELF", body["type"])
		status, body = srv.moveGroup(t, alice, "org", map[string]any{"name": "other"})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "TOP_LEVEL_RENAME", body["type"])
		status, _ = srv.moveGroup(t, alice, "org/squad", map[string]any{"parent": "", "name": "bob"})
		assert.Equal(t, http.StatusConflict, status, "a username")
		status, _ = srv.moveGroup(t, alice, "org/squad/ops", map[string]any{"parent": "org", "name": "squad"})
		assert.Equal(t, http.StatusConflict, status, "taken")
		status, _ = srv.createIn(t, alice, "org", "Repo", true)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.moveGroup(t, alice, "org/squad/ops", map[string]any{"parent": "org", "name": "repo"})
		assert.Equal(t, http.StatusConflict, status, "a repository's path")
		status, _ = srv.moveGroup(t, dave, "org/squad", map[string]any{"parent": "corp"})
		assert.Contains(t, []int{http.StatusForbidden, http.StatusNotFound}, status)

		parent := "corp"
		for _, n := range []string{"a", "b", "c"} {
			status, body = srv.subgroup(t, alice, parent, n)
			require.Equal(t, http.StatusOK, status, "%v", body)
			parent += "/" + n
		}
		status, body = srv.moveGroup(t, alice, "org/squad", map[string]any{"parent": parent})
		assert.Equal(t, http.StatusBadRequest, status, "corp/a/b/c/squad/ops is 6 levels: %v", body)
		assert.Equal(t, "GROUP_TOO_DEEP", body["type"])
		status, _ = srv.moveGroup(t, bob, "org/squad", map[string]any{"name": "x"})
		assert.Equal(t, http.StatusNotFound, status)

		// Owner of the group, but only a member of the target.
		status, body = srv.post(t, "/api/v1/group", bob.session, map[string]any{"name": "bobs"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.subgroup(t, bob, "bobs", "x")
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, groupAPI("corp")+"/members", alice.session, map[string]any{"username": "bob", "role": "member"})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.moveGroup(t, bob, "bobs/x", map[string]any{"parent": "corp"})
		assert.Equal(t, http.StatusForbidden, status, "members of the target don't move groups into it")
	})

	t.Run("move_and_promote", func(t *testing.T) {
		status, body := srv.moveGroup(t, alice, "org/squad", map[string]any{"parent": "corp"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "corp/squad", body["name"])
		status, body = srv.get(t, repoAPI("org/team/app"), alice.session)
		require.Equal(t, http.StatusOK, status, "redirects follow: %v", body)
		assert.Equal(t, "corp/squad/app", body["full_path"])
		status, body = srv.get(t, groupAPI("corp/squad")+"/members", dave.session)
		assert.Equal(t, http.StatusNotFound, status, "dave's role came from org: %v", body)

		status, body = srv.moveGroup(t, alice, "corp/squad", map[string]any{"parent": "", "name": "squad"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "squad", body["name"])
		assert.Nil(t, body["parent"])
		status, body = srv.get(t, repoAPI("squad/ops/tool"), alice.session)
		assert.Equal(t, http.StatusOK, status, "%v", body)

		status, body = srv.moveGroup(t, alice, "org", map[string]any{"parent": "squad"})
		require.Equal(t, http.StatusOK, status, "a top-level group moves under another: %v", body)
		assert.Equal(t, "squad/org", body["name"])
		status, body = srv.get(t, repoAPI("org/repo"), alice.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "squad/org/repo", body["full_path"])
	})

	t.Run("takeover", func(t *testing.T) {
		// A group created at a former path takes it over.
		status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.get(t, groupAPI("org"), alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, "org", body["name"])
	})
}

func TestSubgroupActions(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.subgroup(t, alice, "org", "team")
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.createIn(t, alice, "org/team", "App", true)
	require.Equal(t, http.StatusOK, status, "%v", body)

	put := func(path, value string) {
		t.Helper()
		status, body := srv.request(t, http.MethodPut, path, alice.session, map[string]any{"value": value})
		require.Equal(t, http.StatusOK, status, "%s: %v", path, body)
	}
	put(groupAPI("org")+"/actions/secrets/TOKEN", "org-token")
	put(groupAPI("org")+"/actions/secrets/ORG_ONLY", "o")
	put(groupAPI("org/team")+"/actions/secrets/TOKEN", "team-token")
	put(groupAPI("org")+"/actions/variables/REGION", "eu")
	put(groupAPI("org/team")+"/actions/variables/REGION", "us")
	put(actionsAPI("org/team/app")+"/variables/REGION", "ap")

	runner := srv.registerRunner(t, alice, groupAPI("org")+"/runners/registration-token", "org-runner", "ubuntu-latest")
	pushFiles(t, srv.url+"/org/team/app.git", alice.git, "main", "ci", map[string]string{".github/workflows/ci.yml": ciWorkflow})
	task := fetch0(t, runner, "ubuntu-latest")
	require.NotNil(t, task, "the parent group's runner serves the sub-group's repositories")
	assert.Equal(t, "org/team/app", task.Repository)
	assert.Equal(t, map[string]string{"TOKEN": "team-token", "ORG_ONLY": "o"}, task.Secrets, "the nearest group wins")
	assert.Equal(t, map[string]string{"REGION": "ap"}, task.Vars, "the repository's own win over all")
}

func TestGroupWebhooks(t *testing.T) {
	srv := startTestServer(t, allowLoopback)
	alice, carol := srv.newUser(t, "alice"), srv.newUser(t, "carol")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = srv.post(t, groupAPI("org")+"/members", alice.session, map[string]any{"username": "carol", "role": "member"})
	require.Equal(t, http.StatusOK, status)
	status, body = srv.subgroup(t, alice, "org", "team")
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, body = srv.createIn(t, alice, "org/team", "App", true)
	require.Equal(t, http.StatusOK, status, "%v", body)
	srv.createRepo(t, alice.session, "Mine", true)

	rcv := newHookReceiver(t)
	status, body = srv.post(t, groupAPI("org")+"/hooks", carol.session, map[string]any{"url": rcv.URL, "events": []string{"push"}})
	assert.Equal(t, http.StatusForbidden, status, "members don't manage hooks: %v", body)
	status, hook := srv.post(t, groupAPI("org")+"/hooks", alice.session, map[string]any{"url": rcv.URL, "events": []string{"push"}, "secret": "s"})
	require.Equal(t, http.StatusOK, status, "%v", hook)
	srv.deliver(t)
	got := rcv.takeEvents()
	require.Len(t, got, 1)
	assert.Equal(t, "ping", got[0].event)
	assert.Equal(t, "org", got[0].payload["organization"].(map[string]any)["login"])

	pushFiles(t, srv.url+"/org/team/app.git", alice.git, "main", "x", map[string]string{"a.txt": "a\n"})
	pushFiles(t, srv.url+"/alice/mine.git", alice.git, "main", "x", map[string]string{"a.txt": "a\n"})
	srv.deliver(t)
	got = rcv.takeEvents()
	require.Len(t, got, 1, "only repositories below the group")
	assert.Equal(t, "push", got[0].event)
	assert.Equal(t, "org/team/app", got[0].payload["repository"].(map[string]any)["full_name"])

	status, body = srv.get(t, groupAPI("org")+"/hooks/"+hook["id"].(string)+"/deliveries", alice.session)
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Len(t, body["deliveries"], 2)
	status, _ = srv.get(t, groupAPI("org/team")+"/hooks/"+hook["id"].(string), alice.session)
	assert.Equal(t, http.StatusNotFound, status, "a sub-group doesn't own its parent's hooks")
	status, _ = srv.request(t, http.MethodDelete, groupAPI("org")+"/hooks/"+hook["id"].(string), alice.session, nil)
	assert.Equal(t, http.StatusOK, status)
}

func TestSubgroupPackages(t *testing.T) {
	srv := startTestServer(t)
	alice, carol, erin := srv.newUser(t, "alice"), srv.newUser(t, "carol"), srv.newUser(t, "erin")
	status, body := srv.post(t, "/api/v1/group", alice.session, map[string]any{"name": "org"})
	require.Equal(t, http.StatusOK, status, "%v", body)
	status, _ = srv.post(t, groupAPI("org")+"/members", alice.session, map[string]any{"username": "carol", "role": "member"})
	require.Equal(t, http.StatusOK, status)
	for _, g := range [][2]string{{"org", "web"}, {"org/web", "ui"}} {
		status, body = srv.subgroup(t, alice, g[0], g[1])
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	status, _ = srv.post(t, groupAPI("org/web/ui")+"/members", alice.session, map[string]any{"username": "erin", "role": "member"})
	require.Equal(t, http.StatusOK, status)
	w := srv.createToken(t, alice.session, "p", "write:packages")
	carolR := srv.createToken(t, carol.session, "r", "read:packages")
	erinR := srv.createToken(t, erin.session, "r", "read:packages")
	pkgAPI := func(ns, typ, name string) string {
		return "/api/v1/packages/" + url.PathEscape(ns) + "/" + typ + "/" + url.PathEscape(name)
	}

	t.Run("generic", func(t *testing.T) {
		status, body, _ := srv.generic(t, http.MethodPut, "org/web/generic/tool/1.0.0/tool.bin", w, []byte("tool"))
		require.Equal(t, http.StatusCreated, status, "%s", body)
		status, data, _ := srv.generic(t, http.MethodGet, "org/web/generic/tool/1.0.0/tool.bin", carolR, nil)
		assert.Equal(t, http.StatusOK, status, "an inherited member reads")
		assert.Equal(t, "tool", string(data))
		status, _, _ = srv.generic(t, http.MethodGet, "org/web/generic/tool/1.0.0/tool.bin", erinR, nil)
		assert.Equal(t, http.StatusNotFound, status, "a sub-group member doesn't read the parent's")
		status, _ = srv.get(t, pkgAPI("org/web", "generic", "tool"), alice.session)
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("container", func(t *testing.T) {
		c := srv.registry(t, w)
		c.push("org/web/ui/app", "v1", newTestImage(t, "ui"))
		status, body := srv.get(t, pkgAPI("org/web/ui", "container", "app"), alice.session)
		require.Equal(t, http.StatusOK, status, "the deepest group owns it: %v", body)
		assert.Equal(t, http.StatusOK, srv.registry(t, erinR).pull("org/web/ui/app", "v1"))
		c.push("org/web/images/base", "v1", newTestImage(t, "base"))
		status, _ = srv.get(t, pkgAPI("org/web", "container", "images/base"), alice.session)
		assert.Equal(t, http.StatusOK, status, "no group org/web/images: an image path in org/web")
	})

	t.Run("npm", func(t *testing.T) {
		for _, name := range []string{"@org/web-ui-button", "@org/web-tool", "@org/plain"} {
			tgz := npmTarball(t, name, "1.0.0", "")
			status, body := srv.npmDo(t, http.MethodPut, url.PathEscape(name), w, npmPublishBody(name, "1.0.0", tgz, nil, nil))
			require.Equal(t, http.StatusCreated, status, "%s: %v", name, body)
		}
		for ns, name := range map[string]string{"org/web/ui": "web-ui-button", "org/web": "web-tool", "org": "plain"} {
			status, body := srv.get(t, pkgAPI(ns, "npm", name), alice.session)
			assert.Equal(t, http.StatusOK, status, "%s owns %s: %v", ns, name, body)
		}
		status, doc := srv.npmDo(t, http.MethodGet, "@org%2fweb-ui-button", erinR, nil)
		require.Equal(t, http.StatusOK, status, "%v", doc)
		status, _ = srv.npmDo(t, http.MethodGet, "@org%2fplain", erinR, nil)
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.npmDo(t, http.MethodGet, "@org%2fplain", carolR, nil)
		assert.Equal(t, http.StatusOK, status)
		carolW := srv.createToken(t, carol.session, "w", "write:packages")
		tgz := npmTarball(t, "@org/web-ui-button", "2.0.0", "")
		status, _ = srv.npmDo(t, http.MethodPut, "@org%2fweb-ui-button", carolW, npmPublishBody("@org/web-ui-button", "2.0.0", tgz, nil, nil))
		assert.Equal(t, http.StatusForbidden, status, "an existing name: its owner's writers only")

		// The group moves: the package keeps its name.
		status, body := srv.moveGroup(t, alice, "org/web/ui", map[string]any{"parent": "org"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.npmDo(t, http.MethodGet, "@org%2fweb-ui-button", erinR, nil)
		assert.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, pkgAPI("org/ui", "npm", "web-ui-button"), alice.session)
		assert.Equal(t, http.StatusOK, status, "now in org/ui")
		status, _ = srv.get(t, pkgAPI("org/ui", "container", "app"), alice.session)
		assert.Equal(t, http.StatusOK, status, "images move along")
	})
}
