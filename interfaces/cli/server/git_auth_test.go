package server

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/migration"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	routes "github.com/labbs/ogit/interfaces/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitHTTPAuthorization drives the real server (sqlite + each storage
// backend) with a git client and checks every allow/deny path of the git
// routes. git_auth_integration_test.go adds MinIO.
func TestGitHTTPAuthorization(t *testing.T) {
	t.Run("local", func(t *testing.T) { testGitHTTPAuthorization(t) })
	t.Run("s3_fake", func(t *testing.T) { testGitHTTPAuthorization(t, withS3(s3test.StartFake(t))) })
}

func withS3(ep s3test.Endpoint) func(*config.Config) {
	return func(c *config.Config) { s3test.Configure(c, ep) }
}

func testGitHTTPAuthorization(t *testing.T, opts ...func(*config.Config)) {
	srv := startTestServer(t, opts...)

	owner := srv.registerAndLogin(t, "owner@example.com", "owner")
	outsider := srv.registerAndLogin(t, "outsider@example.com", "outsider")

	srv.createRepo(t, owner, "Private Repo", true)
	srv.createRepo(t, owner, "Public Repo", false)
	private := srv.url + "/owner/private-repo.git"
	public := srv.url + "/owner/public-repo.git"

	ownerRW := srv.createToken(t, owner, "owner-rw", "read:repository", "write:repository")
	ownerRO := srv.createToken(t, owner, "owner-ro", "read:repository")
	outsiderRW := srv.createToken(t, outsider, "outsider-rw", "read:repository", "write:repository")
	wrongSecret := &githttp.BasicAuth{Username: "owner-rw", Password: "ogit_not-the-token"}

	t.Run("anonymous", func(t *testing.T) {
		_, err := clone(t, private, nil)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "clone private")

		repo, err := clone(t, public, nil)
		require.NoError(t, err, "clone public")
		assert.ErrorIs(t, commitAndPush(t, repo, nil), transport.ErrAuthenticationRequired, "push public")

		_, err = clone(t, srv.url+"/owner/does-not-exist.git", nil)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired, "missing repos look like private ones")
	})

	t.Run("invalid_token", func(t *testing.T) {
		_, err := clone(t, private, wrongSecret)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired)
	})

	t.Run("default_branch_is_main", func(t *testing.T) {
		repo, err := clone(t, private, ownerRO)
		require.NoError(t, err)
		head, err := repo.Head()
		require.NoError(t, err)
		assert.Equal(t, plumbing.Main, head.Name())
	})

	t.Run("read_only_token", func(t *testing.T) {
		repo, err := clone(t, private, ownerRO)
		require.NoError(t, err, "clone private")
		assert.ErrorIs(t, commitAndPush(t, repo, ownerRO), transport.ErrAuthorizationFailed, "push without write scope")
	})

	t.Run("owner_read_write_token", func(t *testing.T) {
		repo, err := clone(t, private, ownerRW)
		require.NoError(t, err)
		require.NoError(t, commitAndPush(t, repo, ownerRW))

		head, err := repo.Head()
		require.NoError(t, err)
		fresh, err := clone(t, private, ownerRW)
		require.NoError(t, err)
		freshHead, err := fresh.Head()
		require.NoError(t, err)
		assert.Equal(t, head.Hash(), freshHead.Hash(), "fresh clone sees the push")
	})

	t.Run("non_member", func(t *testing.T) {
		_, err := clone(t, private, outsiderRW)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound, "private repo is hidden from non-members")

		repo, err := clone(t, public, outsiderRW)
		require.NoError(t, err, "anyone can read a public repo")
		assert.ErrorIs(t, commitAndPush(t, repo, outsiderRW), transport.ErrAuthorizationFailed, "non-members cannot push")
	})

	t.Run("is_private_defaults_to_true", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/repository", owner, map[string]any{"name": "Unspecified"})
		require.Equal(t, http.StatusOK, status, "create repo: %v", body)

		_, err := clone(t, srv.url+"/owner/unspecified.git", nil)
		assert.ErrorIs(t, err, transport.ErrAuthenticationRequired)
		_, err = clone(t, srv.url+"/owner/unspecified.git", ownerRO)
		assert.NoError(t, err)
	})

	t.Run("missing_repo_with_valid_token", func(t *testing.T) {
		_, err := clone(t, srv.url+"/owner/does-not-exist.git", ownerRW)
		assert.ErrorIs(t, err, transport.ErrRepositoryNotFound)
	})
}

func TestCreateToken(t *testing.T) {
	srv := startTestServer(t)
	owner := srv.registerAndLogin(t, "owner@example.com", "owner")

	t.Run("requires_session", func(t *testing.T) {
		// fiber-oapi answers unauthenticated requests with 400 on every route.
		status, body := srv.post(t, "/api/v1/user/token", "", map[string]any{"name": "x", "scopes": []string{"read:repository"}})
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "authentication required", body["details"])
	})

	t.Run("rejects_unknown_scope", func(t *testing.T) {
		status, _ := srv.post(t, "/api/v1/user/token", owner, map[string]any{"name": "x", "scopes": []string{"admin:repository"}})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("rejects_past_expiry", func(t *testing.T) {
		status, _ := srv.post(t, "/api/v1/user/token", owner, map[string]any{
			"name": "x", "scopes": []string{"read:repository"}, "expires_at": time.Now().Add(-time.Hour),
		})
		assert.Equal(t, http.StatusBadRequest, status)
	})

	t.Run("stores_only_the_hash", func(t *testing.T) {
		auth := srv.createToken(t, owner, "hashed", "read:repository")
		assert.Regexp(t, `^ogit_[0-9a-f]{64}$`, auth.Password)

		var stored domain.Token
		require.NoError(t, srv.deps.Database.Db.Where("name = ?", "hashed").First(&stored).Error)
		assert.NotEqual(t, auth.Password, stored.Token)
		assert.Equal(t, application.HashToken(auth.Password), stored.Token)
		assert.Equal(t, domain.TokenScopes{domain.ScopeReadRepository}, stored.Scopes)
	})
}

// testDatabases switch every test server to another database. Empty means
// sqlite; db_integration_test.go adds Postgres when OGIT_TEST_DB=postgres.
var testDatabases []func(t *testing.T, cfg *config.Config)

type testServer struct {
	url  string
	deps infrastructure.Deps
}

func startTestServer(t *testing.T, opts ...func(*config.Config)) *testServer {
	t.Helper()
	dir := t.TempDir()

	var cfg config.Config
	cfg.Version = "test"
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "ogit.sqlite")
	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = filepath.Join(dir, "repositories")
	cfg.Session.SecretKey = "test-secret-at-least-32-bytes-long"
	cfg.Session.ExpirationMinutes = 60
	cfg.Session.Issuer = "ogit-test"
	cfg.Registration.Enabled = true
	cfg.Registration.PasswordMinLength = 12
	cfg.Registration.PasswordComplexity = true
	for _, useDatabase := range testDatabases {
		useDatabase(t, &cfg)
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	deps, err := setupDeps(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = deps.CronScheduler.CronScheduler.Shutdown() })
	t.Cleanup(func() {
		if sqlDB, err := deps.Database.Db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, migration.RunMigration(deps.Logger, deps.Database.Db))

	routes.SetupRoutes(deps)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = deps.Http.Fiber.Listener(ln) }()
	t.Cleanup(func() { _ = deps.Http.Fiber.Shutdown() })

	return &testServer{url: "http://" + ln.Addr().String(), deps: deps}
}

func (s *testServer) post(t *testing.T, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, s.url+path, bytes.NewReader(payload))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// registerAndLogin returns a session JWT for a new user.
func (s *testServer) registerAndLogin(t *testing.T, email, username string) string {
	t.Helper()
	const password = "Correct-Horse-Battery-9"
	status, body := s.post(t, "/api/v1/auth/register", "", map[string]any{"email": email, "username": username, "password": password})
	require.Equal(t, http.StatusOK, status, "register: %v", body)
	status, body = s.post(t, "/api/v1/auth/login", "", map[string]any{"email": email, "password": password})
	require.Equal(t, http.StatusOK, status, "login: %v", body)
	return body["token"].(string)
}

// repoAPI is the API URL of a repository addressed by its path
// ("<namespace>/<slug>"); the slash is escaped so it stays one path segment.
func repoAPI(path string) string {
	return "/api/v1/repository/" + url.PathEscape(path)
}

func (s *testServer) createRepo(t *testing.T, bearer, name string, private bool) map[string]any {
	t.Helper()
	status, body := s.post(t, "/api/v1/repository", bearer, map[string]any{"name": name, "is_private": private})
	require.Equal(t, http.StatusOK, status, "create repo: %v", body)
	return body
}

// request sends a JSON request (body may be nil) and decodes the JSON response.
func (s *testServer) request(t *testing.T, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(payload)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, s.url+path, reader)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (s *testServer) get(t *testing.T, path, bearer string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.url+path, nil)
	require.NoError(t, err)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (s *testServer) createToken(t *testing.T, bearer, name string, scopes ...string) *githttp.BasicAuth {
	t.Helper()
	status, body := s.post(t, "/api/v1/user/token", bearer, map[string]any{"name": name, "scopes": scopes})
	require.Equal(t, http.StatusOK, status, "create token: %v", body)
	return &githttp.BasicAuth{Username: name, Password: body["token"].(string)}
}

func clone(t *testing.T, repoURL string, auth *githttp.BasicAuth) (*git.Repository, error) {
	t.Helper()
	opts := &git.CloneOptions{URL: repoURL}
	if auth != nil {
		opts.Auth = auth
	}
	return git.PlainClone(t.TempDir(), false, opts)
}

func commitAndPush(t *testing.T, repo *git.Repository, auth *githttp.BasicAuth) error {
	t.Helper()
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Commit("test commit", &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	})
	require.NoError(t, err)

	opts := &git.PushOptions{}
	if auth != nil {
		opts.Auth = auth
	}
	return repo.Push(opts)
}
