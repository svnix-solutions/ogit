package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func releasesAPI(repo string, rest ...string) string {
	s := repoAPI(repo) + "/releases"
	for _, r := range rest {
		s += "/" + r
	}
	return s
}

func (s *testServer) release(t *testing.T, as user, repo string, body map[string]any) map[string]any {
	t.Helper()
	status, resp := s.post(t, releasesAPI(repo), as.session, body)
	require.Equal(t, http.StatusOK, status, "%v", resp)
	return resp
}

// pushTag tags the tip of branch and pushes the tag.
func pushTag(t *testing.T, url string, as user, branch, tag string) string {
	t.Helper()
	repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: url, Auth: as.git})
	require.NoError(t, err)
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	require.NoError(t, err)
	_, err = repo.CreateTag(tag, ref.Hash(), nil)
	require.NoError(t, err)
	require.NoError(t, repo.Push(&git.PushOptions{Auth: as.git, RefSpecs: []gitconfig.RefSpec{gitconfig.RefSpec("refs/tags/" + tag + ":refs/tags/" + tag)}}))
	return ref.Hash().String()
}

func TestReleases(t *testing.T) {
	t.Run("local", func(t *testing.T) { testReleases(t) })
	t.Run("s3_fake", func(t *testing.T) { testReleases(t, withS3(s3test.StartFake(t))) })
}

func testReleases(t *testing.T, opts ...func(*config.Config)) {
	f := newPullFixture(t, true, append(opts, allowLoopback, withPublicURL)...)
	srv := f.srv
	rcv := newHookReceiver(t)
	srv.createHook(t, f.owner, "owner/app", map[string]any{"url": rcv.URL, "events": []string{"release", "push"}})
	srv.deliver(t)
	rcv.take()
	collect := func() []hookEvent { srv.deliver(t); return rcv.takeEvents() }

	t.Run("on_an_existing_tag", func(t *testing.T) {
		sha := pushTag(t, f.url, f.dev, "main", "v1.0.0")
		collect()
		r := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v1.0.0", "name": "First", "body": "Hello"})
		assert.Equal(t, "v1.0.0", r["tag_name"])
		assert.Equal(t, false, r["draft"])
		assert.NotNil(t, r["published_at"])
		assert.Equal(t, "dev", r["author"])
		assert.Equal(t, sha, srv.refs(t, f.owner, "owner/app")["tags/v1.0.0"])
		evs := collect()
		assert.ElementsMatch(t, []string{"release.created", "release.published", "release.released"}, names(evs))
		rel := find(t, evs, "release.published").payload["release"].(map[string]any)
		assert.Equal(t, "https://git.example.com/owner/app/archive/v1.0.0.zip", rel["zipball_url"])
		assert.Equal(t, "dev", rel["author"].(map[string]any)["login"])
	})

	t.Run("creating_the_tag", func(t *testing.T) {
		tip := pushFiles(t, f.url, f.dev.git, "main", "more", map[string]string{"b.txt": "b\n"})
		collect()
		r := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v1.1.0", "target_commitish": "main", "prerelease": true})
		assert.Equal(t, true, r["prerelease"])
		assert.Equal(t, tip, srv.refs(t, f.owner, "owner/app")["tags/v1.1.0"], "the tag is created on the branch tip")
		evs := collect()
		assert.ElementsMatch(t, []string{"push.", "release.created", "release.published", "release.prereleased"}, names(evs))
		assert.Equal(t, "refs/tags/v1.1.0", find(t, evs, "push.").payload["ref"])

		_, latest := srv.get(t, releasesAPI("owner/app", "latest"), f.reporter.session)
		assert.Equal(t, "v1.0.0", latest["tag_name"], "pre-releases are never latest")
	})

	var draftId string
	t.Run("drafts", func(t *testing.T) {
		d := srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v2.0.0", "draft": true, "name": "Next"})
		draftId = d["id"].(string)
		assert.Nil(t, d["published_at"])
		assert.Empty(t, srv.refs(t, f.owner, "owner/app")["tags/v2.0.0"], "no tag until published")
		assert.Empty(t, collect(), "no events for drafts")

		_, list := srv.get(t, releasesAPI("owner/app"), f.reporter.session)
		assert.Len(t, list["releases"], 2, "readers don't see drafts")
		status, _ := srv.get(t, releasesAPI("owner/app", draftId), f.reporter.session)
		assert.Equal(t, http.StatusNotFound, status)
		_, list = srv.get(t, releasesAPI("owner/app"), f.dev.session)
		assert.Len(t, list["releases"], 3)
		assert.Equal(t, "v2.0.0", list["releases"].([]any)[0].(map[string]any)["tag_name"], "drafts first")

		status, body := srv.request(t, http.MethodPatch, releasesAPI("owner/app", draftId), f.dev.session, map[string]any{"tag_name": "v2.0.0-rc1"})
		require.Equal(t, http.StatusOK, status, "a draft may change its tag: %v", body)
		status, body = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draftId), f.dev.session, map[string]any{"draft": false})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotEmpty(t, srv.refs(t, f.owner, "owner/app")["tags/v2.0.0-rc1"], "publishing creates the tag")
		assert.ElementsMatch(t, []string{"push.", "release.created", "release.published", "release.released"}, names(collect()))
		_, latest := srv.get(t, releasesAPI("owner/app", "latest"), f.reporter.session)
		assert.Equal(t, "v2.0.0-rc1", latest["tag_name"])

		status, _ = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draftId), f.dev.session, map[string]any{"tag_name": "v9"})
		assert.Equal(t, http.StatusBadRequest, status, "a published release keeps its tag")
		status, _ = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draftId), f.dev.session, map[string]any{"draft": true})
		assert.Equal(t, http.StatusBadRequest, status, "and stays published")
		status, body = srv.request(t, http.MethodPatch, releasesAPI("owner/app", draftId), f.dev.session, map[string]any{"name": "Two"})
		require.Equal(t, http.StatusOK, status, "%v", body)
		evs := collect()
		require.Equal(t, []string{"release.edited"}, names(evs))
		assert.Equal(t, "Next", evs[0].payload["changes"].(map[string]any)["name"].(map[string]any)["from"])
	})

	t.Run("validation_and_permissions", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"tag_name": "bad tag"}, {"tag_name": "-x"}, {"tag_name": "a..b"}, {"tag_name": "x.lock"}, {"tag_name": ""},
			{"tag_name": "v3", "target_commitish": "no-such-branch"},
		} {
			status, resp := srv.post(t, releasesAPI("owner/app"), f.dev.session, body)
			assert.Equal(t, http.StatusBadRequest, status, "%v: %v", body, resp)
		}
		status, body := srv.post(t, releasesAPI("owner/app"), f.dev.session, map[string]any{"tag_name": "v1.0.0"})
		assert.Equal(t, http.StatusConflict, status, "%v", body)
		status, _ = srv.post(t, releasesAPI("owner/app"), f.reporter.session, map[string]any{"tag_name": "v3"})
		assert.Equal(t, http.StatusForbidden, status, "developers and up")
		status, _ = srv.get(t, releasesAPI("owner/app"), f.outer.session)
		assert.Equal(t, http.StatusNotFound, status, "a private repository stays hidden")
		status, _ = srv.post(t, releasesAPI("owner/app"), "", map[string]any{"tag_name": "v3"})
		assert.NotEqual(t, http.StatusOK, status)

		ci := srv.repoToken(t, f.maint, "owner/app", "ci", "read:repository", "write:repository")
		status, body, _ = srv.do(t, http.MethodPost, releasesAPI("owner/app"), tokenHeader(ci["token"].(string)), map[string]any{"tag_name": "v3.0.0"})
		require.Equal(t, http.StatusOK, status, "CI creates releases with a repository token: %v", body)
		assert.Empty(t, body["author"])
		ro := srv.repoToken(t, f.maint, "owner/app", "ro", "read:repository")
		status, _, _ = srv.do(t, http.MethodPost, releasesAPI("owner/app"), tokenHeader(ro["token"].(string)), map[string]any{"tag_name": "v3.0.1"})
		assert.Equal(t, http.StatusForbidden, status, "read-only tokens can't")
		status, _, _ = srv.do(t, http.MethodGet, releasesAPI("owner/app", "by-tag")+"?tag=v3.0.0", tokenHeader(ro["token"].(string)), nil)
		assert.Equal(t, http.StatusOK, status, "but read")
		collect()
	})

	t.Run("delete_keeps_the_tag", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodDelete, releasesAPI("owner/app", draftId), f.reporter.session, nil)
		assert.Equal(t, http.StatusForbidden, status)
		status, _ = srv.request(t, http.MethodDelete, releasesAPI("owner/app", draftId), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.NotEmpty(t, srv.refs(t, f.owner, "owner/app")["tags/v2.0.0-rc1"])
		assert.Equal(t, []string{"release.deleted"}, names(collect()))
		status, _ = srv.get(t, releasesAPI("owner/app", "by-tag")+"?tag=v2.0.0-rc1", f.dev.session)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("archived", func(t *testing.T) {
		status, _ := srv.request(t, http.MethodPatch, repoAPI("owner/app"), f.owner.session, map[string]any{"archived": true})
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.post(t, releasesAPI("owner/app"), f.owner.session, map[string]any{"tag_name": "v4"})
		assert.Equal(t, http.StatusForbidden, status)
		_, list := srv.get(t, releasesAPI("owner/app"), f.owner.session)
		for _, r := range list["releases"].([]any) {
			assert.Equal(t, false, r.(map[string]any)["can_edit"])
		}
	})
}

func TestReleaseNotes(t *testing.T) {
	f := newPullFixture(t, false)
	srv := f.srv
	merge := func(branch, title string) {
		status, pr := f.open(t, f.dev, branch, map[string]string{branch + ".txt": branch + "\n"}, map[string]any{"title": title})
		require.Equal(t, http.StatusOK, status, "%v", pr)
		status, body := srv.post(t, pullsAPI("owner/app", pr["number"], "merge"), f.maint.session, map[string]any{"method": "merge"})
		require.Equal(t, http.StatusOK, status, "%v", body)
	}
	merge("old", "Old work")
	srv.release(t, f.dev, "owner/app", map[string]any{"tag_name": "v1.0.0"})
	merge("feature-a", "Add A")
	merge("feature-b", "Add B")

	status, notes := srv.post(t, releasesAPI("owner/app", "generate-notes"), f.dev.session, map[string]any{"tag_name": "v1.1.0"})
	require.Equal(t, http.StatusOK, status, "%v", notes)
	body := notes["body"].(string)
	assert.Equal(t, "v1.1.0", notes["name"])
	assert.Contains(t, body, "* Add A by @dev in #2")
	assert.Contains(t, body, "* Add B by @dev in #3")
	assert.NotContains(t, body, "Old work", "already in v1.0.0")
	assert.Less(t, strings.Index(body, "Add A"), strings.Index(body, "Add B"), "oldest first")
	assert.Contains(t, body, "**Full Changelog**: v1.0.0...v1.1.0")

	status, notes = srv.post(t, releasesAPI("owner/app", "generate-notes"), f.dev.session, map[string]any{"tag_name": "v1.1.0", "previous_tag_name": "nope"})
	assert.Equal(t, http.StatusNotFound, status, "%v", notes)
	status, _ = srv.post(t, releasesAPI("owner/app", "generate-notes"), f.reporter.session, map[string]any{"tag_name": "v1.1.0"})
	assert.Equal(t, http.StatusForbidden, status)
}
