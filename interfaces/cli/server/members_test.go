package server

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// user is a registered account with an API session and a read/write git token.
type user struct {
	name    string
	session string
	git     *githttp.BasicAuth
}

func (s *testServer) newUser(t *testing.T, name string) user {
	t.Helper()
	session := s.registerAndLogin(t, name+"@example.com", name)
	return user{name: name, session: session, git: s.createToken(t, session, name+"-git", "read:repository", "write:repository")}
}

// members returns username-or-group → {id, role} for a repository, given by
// id or path ("<namespace>/<slug>").
func (s *testServer) members(t *testing.T, repo string, as user) map[string]map[string]any {
	t.Helper()
	status, body := s.get(t, repoAPI(repo)+"/members", as.session)
	require.Equal(t, http.StatusOK, status, "list members: %v", body)
	out := map[string]map[string]any{}
	for _, raw := range body["members"].([]any) {
		m := raw.(map[string]any)
		key, _ := m["username"].(string)
		if key == "" {
			key = "group:" + m["group"].(string)
		}
		out[key] = m
	}
	return out
}

func (s *testServer) memberID(t *testing.T, repo string, as user, key string) string {
	t.Helper()
	m, ok := s.members(t, repo, as)[key]
	require.True(t, ok, "%s is a member of %s", key, repo)
	return m["id"].(string)
}

func TestRepositoryMembers(t *testing.T) {
	srv := startTestServer(t)
	owner, maint, dev, _, bob, outsider := srv.newUser(t, "owner"), srv.newUser(t, "maint"), srv.newUser(t, "dev"),
		srv.newUser(t, "alice"), srv.newUser(t, "bob"), srv.newUser(t, "outsider")
	srv.createRepo(t, owner.session, "Team Repo", true)
	srv.createRepo(t, owner.session, "Open Repo", false)
	const repo = "owner/team-repo"
	membersURL := repoAPI(repo) + "/members"
	add := func(as user, body map[string]any) (int, map[string]any) {
		return srv.request(t, http.MethodPost, membersURL, as.session, body)
	}
	patch := func(as user, id, role string) int {
		status, _ := srv.request(t, http.MethodPatch, membersURL+"/"+id, as.session, map[string]any{"role": role})
		return status
	}
	remove := func(as user, id string) int {
		status, _ := srv.request(t, http.MethodDelete, membersURL+"/"+id, as.session, nil)
		return status
	}

	t.Run("creator_is_the_only_owner", func(t *testing.T) {
		m := srv.members(t, repo, owner)
		require.Len(t, m, 1)
		assert.Equal(t, "owner", m["owner"]["role"])
		assert.Equal(t, "user", m["owner"]["type"])
	})

	t.Run("member_list_visibility", func(t *testing.T) {
		status, _ := srv.get(t, membersURL, outsider.session)
		assert.Equal(t, http.StatusNotFound, status, "private repo members are hidden")
		status, _ = srv.get(t, repoAPI("owner/open-repo")+"/members", outsider.session)
		assert.Equal(t, http.StatusOK, status, "public repo members are visible to readers")
	})

	t.Run("owner_adds_members", func(t *testing.T) {
		status, body := add(owner, map[string]any{"username": "maint", "role": "maintainer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "maint", body["username"])
		assert.Equal(t, "maintainer", body["role"])
		status, _ = add(owner, map[string]any{"username": "dev", "role": "developer"})
		require.Equal(t, http.StatusOK, status)
	})

	t.Run("input_validation", func(t *testing.T) {
		status, _ := add(owner, map[string]any{"username": "alice", "role": "admin"})
		assert.Equal(t, http.StatusBadRequest, status, "unknown role")
		status, _ = add(owner, map[string]any{"username": "alice", "role": "none"})
		assert.Equal(t, http.StatusBadRequest, status, "none is not assignable")
		status, _ = add(owner, map[string]any{"username": "alice", "group": "x", "role": "reporter"})
		assert.Equal(t, http.StatusBadRequest, status, "both username and group")
		status, _ = add(owner, map[string]any{"role": "reporter"})
		assert.Equal(t, http.StatusBadRequest, status, "neither username nor group")
		status, _ = add(owner, map[string]any{"username": "nobody", "role": "reporter"})
		assert.Equal(t, http.StatusNotFound, status, "unknown user")
		status, _ = add(owner, map[string]any{"username": "dev", "role": "reporter"})
		assert.Equal(t, http.StatusConflict, status, "already a member")
	})

	t.Run("maintainer_is_capped_at_developer", func(t *testing.T) {
		status, _ := add(maint, map[string]any{"username": "alice", "role": "reporter"})
		require.Equal(t, http.StatusOK, status, "maintainers add up to developer")
		aliceID := srv.memberID(t, repo, owner, "alice")
		assert.Equal(t, http.StatusOK, patch(maint, aliceID, "developer"))
		assert.Equal(t, http.StatusForbidden, patch(maint, aliceID, "maintainer"), "cannot grant maintainer")

		status, _ = add(maint, map[string]any{"username": "bob", "role": "maintainer"})
		assert.Equal(t, http.StatusForbidden, status, "cannot add a maintainer")
		assert.Equal(t, http.StatusForbidden, patch(maint, srv.memberID(t, repo, owner, "owner"), "developer"), "cannot touch owners")
		assert.Equal(t, http.StatusForbidden, remove(maint, srv.memberID(t, repo, owner, "owner")), "cannot remove owners")

		assert.Equal(t, http.StatusOK, remove(maint, aliceID), "can remove members up to developer")
	})

	t.Run("developers_cannot_manage", func(t *testing.T) {
		status, _ := add(dev, map[string]any{"username": "alice", "role": "reporter"})
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = add(outsider, map[string]any{"username": "alice", "role": "reporter"})
		assert.Equal(t, http.StatusNotFound, status, "non-readers do not learn the repo exists")
	})

	t.Run("roles_control_git_access", func(t *testing.T) {
		repoURL := srv.url + "/" + repo + ".git"
		r, err := clone(t, repoURL, dev.git)
		require.NoError(t, err)
		assert.NoError(t, commitAndPush(t, r, dev.git), "developers push")

		status, _ := add(owner, map[string]any{"username": "bob", "role": "reporter"})
		require.Equal(t, http.StatusOK, status)
		r, err = clone(t, repoURL, bob.git)
		require.NoError(t, err, "reporters clone")
		assert.ErrorIs(t, commitAndPush(t, r, bob.git), transport.ErrAuthorizationFailed, "reporters cannot push")

		_, err = clone(t, repoURL, outsider.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound)
	})

	t.Run("never_zero_owners", func(t *testing.T) {
		ownerID := srv.memberID(t, repo, owner, "owner")
		assert.Equal(t, http.StatusConflict, patch(owner, ownerID, "maintainer"), "last owner cannot be demoted")
		assert.Equal(t, http.StatusConflict, remove(owner, ownerID), "last owner cannot leave")

		assert.Equal(t, http.StatusOK, patch(owner, srv.memberID(t, repo, owner, "maint"), "owner"), "owners can grant owner")
		assert.Equal(t, http.StatusOK, patch(owner, ownerID, "maintainer"), "with a second owner, the first can step down")
		assert.Equal(t, http.StatusConflict, patch(maint, srv.memberID(t, repo, maint, "maint"), "developer"), "the new last owner is protected too")
	})

	t.Run("anyone_can_leave", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, remove(bob, srv.memberID(t, repo, owner, "bob")), "reporters can remove themselves")
		_, ok := srv.members(t, repo, owner)["bob"]
		assert.False(t, ok)
	})

	t.Run("list_my_repositories", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository", dev.session)
		require.Equal(t, http.StatusOK, status)
		repos := body["repositories"].([]any)
		require.Len(t, repos, 1)
		assert.Equal(t, "team-repo", repos[0].(map[string]any)["slug"])
		assert.Equal(t, repo, repos[0].(map[string]any)["full_path"])

		status, body = srv.get(t, "/api/v1/repository", outsider.session)
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, body["repositories"], "public repos are not 'mine' without membership")
	})
}

func TestGroups(t *testing.T) {
	srv := startTestServer(t)
	owner, gm, alice, bob, outsider := srv.newUser(t, "owner"), srv.newUser(t, "gm"), srv.newUser(t, "alice"),
		srv.newUser(t, "bob"), srv.newUser(t, "outsider")
	groupURL := "/api/v1/group/team"
	addMember := func(as user, username, role string) int {
		status, _ := srv.request(t, http.MethodPost, groupURL+"/members", as.session, map[string]any{"username": username, "role": role})
		return status
	}
	groupMembers := func(as user) map[string]string {
		status, body := srv.get(t, groupURL+"/members", as.session)
		require.Equal(t, http.StatusOK, status, "%v", body)
		out := map[string]string{}
		for _, raw := range body["members"].([]any) {
			m := raw.(map[string]any)
			out[m["username"].(string)] = m["role"].(string)
		}
		return out
	}

	t.Run("create", func(t *testing.T) {
		status, body := srv.request(t, http.MethodPost, "/api/v1/group", owner.session, map[string]any{"name": "team", "description": "the team"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "owner", body["role"])

		status, _ = srv.request(t, http.MethodPost, "/api/v1/group", alice.session, map[string]any{"name": "team"})
		assert.Equal(t, http.StatusConflict, status, "names are unique")
		for _, bad := range []string{"Team", "-team", "my team", "a/b", ""} {
			status, _ = srv.request(t, http.MethodPost, "/api/v1/group", alice.session, map[string]any{"name": bad})
			assert.Equal(t, http.StatusBadRequest, status, "name %q", bad)
		}
	})

	t.Run("visible_to_members_only", func(t *testing.T) {
		status, _ := srv.get(t, groupURL, outsider.session)
		assert.Equal(t, http.StatusNotFound, status)
		status, _ = srv.get(t, groupURL+"/members", outsider.session)
		assert.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, map[string]string{"owner": "owner"}, groupMembers(owner))
	})

	t.Run("owner_and_maintainers_manage_members", func(t *testing.T) {
		require.Equal(t, http.StatusOK, addMember(owner, "gm", "maintainer"))
		assert.Equal(t, http.StatusOK, addMember(gm, "alice", "member"), "maintainers add members")
		assert.Equal(t, http.StatusForbidden, addMember(gm, "bob", "maintainer"), "only the owner adds maintainers")
		assert.Equal(t, http.StatusForbidden, addMember(alice, "bob", "member"), "members cannot add")
		assert.Equal(t, http.StatusConflict, addMember(owner, "alice", "member"))
		assert.Equal(t, http.StatusBadRequest, addMember(owner, "bob", "owner"), "ownership only by transfer")

		require.Equal(t, http.StatusOK, addMember(gm, "bob", "member"))
		status, _ := srv.request(t, http.MethodDelete, groupURL+"/members/bob", gm.session, nil)
		assert.Equal(t, http.StatusOK, status, "maintainers remove members")
		status, _ = srv.request(t, http.MethodPatch, groupURL+"/members/alice", gm.session, map[string]any{"role": "maintainer"})
		assert.Equal(t, http.StatusForbidden, status, "only the owner changes roles")

		require.Equal(t, http.StatusOK, addMember(owner, "outsider", "maintainer"))
		status, _ = srv.request(t, http.MethodDelete, groupURL+"/members/outsider", gm.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "maintainers cannot remove maintainers")
		status, _ = srv.request(t, http.MethodDelete, groupURL+"/members/outsider", owner.session, nil)
		require.Equal(t, http.StatusOK, status, "the owner can")
		assert.Equal(t, map[string]string{"owner": "owner", "gm": "maintainer", "alice": "member"}, groupMembers(alice))
	})

	t.Run("group_access_to_repositories", func(t *testing.T) {
		srv.createRepo(t, owner.session, "Group Repo", true)
		membersURL := repoAPI("owner/group-repo") + "/members"
		repoURL := srv.url + "/owner/group-repo.git"

		_, err := clone(t, repoURL, alice.git)
		require.ErrorIs(t, err, transport.ErrRepositoryNotFound, "no access before the group is added")

		status, _ := srv.request(t, http.MethodPost, membersURL, owner.session, map[string]any{"group": "team", "role": "owner"})
		assert.Equal(t, http.StatusBadRequest, status, "groups are at most maintainer")
		status, body := srv.request(t, http.MethodPost, membersURL, owner.session, map[string]any{"group": "team", "role": "developer"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "group", body["type"])
		assert.Equal(t, "team", body["group"])

		r, err := clone(t, repoURL, alice.git)
		require.NoError(t, err, "group members get the group's role")
		assert.NoError(t, commitAndPush(t, r, alice.git))

		status, body = srv.get(t, "/api/v1/repository", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["repositories"], 1, "repos via groups are listed")

		groupID := srv.memberID(t, "owner/group-repo", owner, "group:team")
		status, _ = srv.request(t, http.MethodPatch, membersURL+"/"+groupID, owner.session, map[string]any{"role": "owner"})
		assert.Equal(t, http.StatusBadRequest, status, "cannot promote a group to owner")

		srv.createRepo(t, outsider.session, "Outsider Repo", true)
		status, _ = srv.request(t, http.MethodPost, repoAPI("outsider/outsider-repo")+"/members", outsider.session, map[string]any{"group": "team", "role": "reporter"})
		assert.Equal(t, http.StatusNotFound, status, "only groups you belong to can be added")

		status, _ = srv.request(t, http.MethodDelete, groupURL+"/members/alice", gm.session, nil)
		require.Equal(t, http.StatusOK, status)
		_, err = clone(t, repoURL, alice.git)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound, "leaving the group removes the access")
	})

	t.Run("ownership", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, groupURL+"/members/owner", owner.session, nil)
		assert.Equal(t, http.StatusConflict, status, "the owner cannot leave")
		status, _ = srv.request(t, http.MethodPost, groupURL+"/transfer", owner.session, map[string]any{"username": "outsider"})
		assert.Equal(t, http.StatusNotFound, status, "new owner must be a member")
		status, _ = srv.request(t, http.MethodPost, groupURL+"/transfer", gm.session, map[string]any{"username": "gm"})
		assert.Equal(t, http.StatusForbidden, status, "only the owner transfers")

		status, _ = srv.request(t, http.MethodPost, groupURL+"/transfer", owner.session, map[string]any{"username": "gm"})
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, map[string]string{"owner": "maintainer", "gm": "owner"}, groupMembers(gm))

		status, _ = srv.request(t, http.MethodDelete, groupURL+"/members/owner", owner.session, nil)
		assert.Equal(t, http.StatusOK, status, "the former owner can now leave")
	})

	t.Run("list_my_groups", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/group", gm.session)
		require.Equal(t, http.StatusOK, status)
		assert.Len(t, body["groups"], 1)
		status, body = srv.get(t, "/api/v1/group", outsider.session)
		require.Equal(t, http.StatusOK, status)
		assert.Empty(t, body["groups"])
	})

	t.Run("delete", func(t *testing.T) {
		require.Equal(t, http.StatusOK, addMember(gm, "bob", "member"))
		status, _ := srv.request(t, http.MethodDelete, groupURL, bob.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "only the owner deletes")

		status, _ = srv.request(t, http.MethodDelete, groupURL, gm.session, nil)
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.get(t, groupURL, gm.session)
		assert.Equal(t, http.StatusNotFound, status)
		_, ok := srv.members(t, "owner/group-repo", owner)["group:team"]
		assert.False(t, ok, "deleting a group removes its repository access")
	})
}

// Two owners demoting each other at the same time must not leave the
// repository without an owner (owner rows are locked on postgres; sqlite
// serializes writes).
func TestLastOwnerConcurrentDemotion(t *testing.T) {
	// Heavy and independent: runs alongside the other slow tests, after the
	// sequential ones.
	t.Parallel()
	srv := startTestServer(t)
	a, b := srv.newUser(t, "a"), srv.newUser(t, "b")

	for i := 0; i < 15; i++ {
		name := fmt.Sprintf("race-%d", i)
		srv.createRepo(t, a.session, name, true)
		path := "a/" + name
		membersURL := repoAPI(path) + "/members"
		status, _ := srv.request(t, http.MethodPost, membersURL, a.session, map[string]any{"username": "b", "role": "owner"})
		require.Equal(t, http.StatusOK, status)
		aID, bID := srv.memberID(t, path, a, "a"), srv.memberID(t, path, a, "b")

		var wg sync.WaitGroup
		for _, d := range []struct {
			as user
			id string
		}{{a, bID}, {b, aID}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.request(t, http.MethodPatch, membersURL+"/"+d.id, d.as.session, map[string]any{"role": "developer"})
			}()
		}
		wg.Wait()

		owners := 0
		for _, m := range srv.members(t, path, a) {
			if m["role"] == "owner" {
				owners++
			}
		}
		require.Equal(t, 1, owners, "exactly one demotion may succeed (iteration %d)", i)
	}
}
