package server

import (
	"net/http"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-git/go-git/v5/plumbing/transport"
)

func TestGetRepository(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.registerAndLogin(t, "owner@example.com", "owner")
	outsider := srv.registerAndLogin(t, "outsider@example.com", "outsider")

	private := srv.createRepo(t, owner, "Private Repo", true)
	srv.createRepo(t, owner, "Public Repo", false)

	t.Run("by_path", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("owner/private-repo"), owner)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "Private Repo", body["name"])
		assert.Equal(t, true, body["is_private"])
	})

	t.Run("by_id", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/repository/"+private["id"].(string), owner)
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, "private-repo", body["slug"])
	})

	t.Run("missing", func(t *testing.T) {
		status, _ := srv.get(t, repoAPI("owner/does-not-exist"), owner)
		assert.Equal(t, http.StatusNotFound, status)
	})

	t.Run("private_repo_hidden_from_non_members", func(t *testing.T) {
		status, _ := srv.get(t, repoAPI("owner/private-repo"), outsider)
		assert.Equal(t, http.StatusNotFound, status, "by path")
		status, _ = srv.get(t, "/api/v1/repository/"+private["id"].(string), outsider)
		assert.Equal(t, http.StatusNotFound, status, "by id")
	})

	t.Run("public_repo_visible_to_others", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("owner/public-repo"), outsider)
		assert.Equal(t, http.StatusOK, status, "%v", body)
	})

	t.Run("anonymous_sees_public_only", func(t *testing.T) {
		status, body := srv.get(t, repoAPI("owner/public-repo"), "")
		assert.Equal(t, http.StatusOK, status, "public repositories are browsable signed out")
		assert.Equal(t, "none", body["role"])
		status, _ = srv.get(t, repoAPI("owner/private-repo"), "")
		assert.Equal(t, http.StatusNotFound, status)
	})
}

// Repository-scoped tokens (type "repository") could not be stored before:
// the code used "repo", which the token.type constraint rejects.
func TestRepositoryToken(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.registerAndLogin(t, "owner@example.com", "owner")
	repo := srv.createRepo(t, owner, "Deploy Target", true)
	srv.createRepo(t, owner, "Other Repo", true)

	repoID := repo["id"].(string)
	const plaintext = "ogit_repository-token-for-tests"
	token := &domain.Token{
		Name:         "deploy",
		Token:        application.HashToken(plaintext),
		Type:         domain.TokenRepo,
		RepositoryId: &repoID,
		Active:       true,
		Scopes:       domain.TokenScopes{domain.ScopeReadRepository},
	}
	require.NoError(t, srv.deps.TokenApp.Create(token))

	auth := &githttp.BasicAuth{Username: "deploy", Password: plaintext}
	_, err := clone(t, srv.url+"/owner/deploy-target.git", auth)
	assert.NoError(t, err, "token works on its repository")

	_, err = clone(t, srv.url+"/owner/other-repo.git", auth)
	assert.ErrorIs(t, err, transport.ErrRepositoryNotFound, "token is limited to its repository")
}

// A new repository's README is titled with its name (it used to show the
// storage key on local storage).
func TestNewRepositoryReadme(t *testing.T) {
	for name, opts := range map[string][]func(*config.Config){"local": nil, "s3_fake": {withS3(s3test.StartFake(t))}} {
		t.Run(name, func(t *testing.T) {
			srv := startTestServer(t, opts...)
			alice := srv.newUser(t, "alice")
			srv.createRepo(t, alice.session, "My Project", false)
			status, body := srv.get(t, repoAPI("alice/my-project")+"/tree?spec=", "")
			require.Equal(t, http.StatusOK, status, "%v", body)
			assert.Equal(t, "# My Project\n", body["readme"].(map[string]any)["content"])
		})
	}
}
