package local

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRepositoryHasOnlyMain(t *testing.T) {
	var cfg config.Config
	cfg.Storage.Local.Path = filepath.Join(t.TempDir(), "repos")
	ls := NewLocalStorage(zerolog.Nop(), cfg)
	require.NoError(t, ls.Configure())
	require.NoError(t, ls.CreateRepository("demo", "Test"))

	repo, err := git.PlainOpen(filepath.Join(cfg.Storage.Local.Path, "demo.git"))
	require.NoError(t, err)

	refs, err := repo.References()
	require.NoError(t, err)
	var names []string
	require.NoError(t, refs.ForEach(func(r *plumbing.Reference) error {
		names = append(names, r.Name().String())
		return nil
	}))
	assert.ElementsMatch(t, []string{"HEAD", "refs/heads/main"}, names, "no stray refs/remotes/*")

	remotes, err := repo.Remotes()
	require.NoError(t, err)
	assert.Empty(t, remotes, "no origin pointing at the temp directory")
}
