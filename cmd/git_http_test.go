package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/gofiber/fiber/v2"
	"github.com/labbs/git-server-s3/internal/api/router"
	"github.com/labbs/git-server-s3/internal/config"
	"github.com/labbs/git-server-s3/internal/testutil/s3test"
	"github.com/labbs/git-server-s3/pkg/storage"
	"github.com/labbs/git-server-s3/pkg/storage/local"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGitOverHTTP_Local is the baseline for the S3 variants: the same
// clone/push scenario against the local filesystem backend.
func TestGitOverHTTP_Local(t *testing.T) {
	prev := config.Storage
	t.Cleanup(func() { config.Storage = prev })
	config.Storage.Type = "local"
	config.Storage.Local.Path = t.TempDir()

	str := local.NewLocalStorage(zerolog.Nop())
	require.NoError(t, str.Configure())
	testGitOverHTTP(t, str)
}

func TestGitOverHTTP_S3Fake(t *testing.T) {
	testGitOverHTTP(t, s3test.NewStorage(t, s3test.StartFake(t)))
}

// testGitOverHTTP serves str over Smart HTTP on a random port and drives it
// with the go-git client: clone, commit, push, then a fresh clone to check
// that the push landed.
func testGitOverHTTP(t *testing.T, str storage.GitRepositoryStorage) {
	baseURL := startServer(t, str)

	resp, err := http.Post(baseURL+"/api/repo", "application/json", bytes.NewBufferString(`{"name":"e2e"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	repoURL := baseURL + "/e2e.git"
	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}

	// Every backend must create new repositories on main.
	branch := plumbing.Main
	var pushed plumbing.Hash
	t.Run("clone_initial_commit", func(t *testing.T) {
		dir := t.TempDir()
		repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: repoURL})
		require.NoError(t, err)

		head, err := repo.Head()
		require.NoError(t, err)
		assert.Equal(t, branch, head.Name())
		assert.FileExists(t, filepath.Join(dir, "README.md"))
	})

	t.Run("push_commit", func(t *testing.T) {
		dir := t.TempDir()
		repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: repoURL})
		require.NoError(t, err)

		wt, err := repo.Worktree()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello from e2e\n"), 0o644))
		_, err = wt.Add("hello.txt")
		require.NoError(t, err)
		pushed, err = wt.Commit("add hello.txt", &git.CommitOptions{Author: sig})
		require.NoError(t, err)

		require.NoError(t, repo.Push(&git.PushOptions{}))
	})

	t.Run("fresh_clone_sees_push", func(t *testing.T) {
		require.False(t, pushed.IsZero(), "push_commit must succeed first")

		dir := t.TempDir()
		repo, err := git.PlainClone(dir, false, &git.CloneOptions{URL: repoURL})
		require.NoError(t, err)

		head, err := repo.Head()
		require.NoError(t, err)
		assert.Equal(t, branch, head.Name())
		assert.Equal(t, pushed, head.Hash())

		content, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
		require.NoError(t, err)
		assert.Equal(t, "hello from e2e\n", string(content))
	})

	t.Run("push_new_branch", func(t *testing.T) {
		require.False(t, pushed.IsZero(), "push_commit must succeed first")

		repo, err := git.PlainClone(t.TempDir(), false, &git.CloneOptions{URL: repoURL})
		require.NoError(t, err)

		refspec := gitconfig.RefSpec(branch.String() + ":refs/heads/feature")
		require.NoError(t, repo.Push(&git.PushOptions{RefSpecs: []gitconfig.RefSpec{refspec}}))

		remote, err := repo.Remote("origin")
		require.NoError(t, err)
		refs, err := remote.List(&git.ListOptions{})
		require.NoError(t, err)

		byName := map[plumbing.ReferenceName]plumbing.Hash{}
		for _, r := range refs {
			byName[r.Name()] = r.Hash()
		}
		assert.Equal(t, pushed, byName["refs/heads/feature"])
	})
}

func startServer(t *testing.T, str storage.GitRepositoryStorage) string {
	t.Helper()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	(&router.Config{Fiber: app, Logger: zerolog.Nop(), Storage: str}).Configure()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = app.Listener(ln) }()
	t.Cleanup(func() { _ = app.Shutdown() })

	return "http://" + ln.Addr().String()
}
