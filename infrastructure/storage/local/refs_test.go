package local

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReferencesAreReplacedAtomically(t *testing.T) {
	var cfg config.Config
	cfg.Storage.Local.Path = filepath.Join(t.TempDir(), "repos")
	ls := NewLocalStorage(zerolog.Nop(), cfg)
	require.NoError(t, ls.Configure())
	require.NoError(t, ls.CreateRepository("r"))
	st, err := ls.GetStorer("r")
	require.NoError(t, err)
	name := plumbing.NewBranchReferenceName("main")
	hashes := []plumbing.Hash{plumbing.NewHash("1111111111111111111111111111111111111111"), plumbing.NewHash("2222222222222222222222222222222222222222")}

	initial, err := st.Reference(name)
	require.NoError(t, err)
	valid := append([]plumbing.Hash{initial.Hash()}, hashes...)

	var done atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			require.NoError(t, st.SetReference(plumbing.NewHashReference(name, hashes[i%2])))
		}
		done.Store(true)
	}()
	reads := 0
	for !done.Load() {
		ref, err := st.Reference(name)
		require.NoError(t, err, "a reader never sees the branch missing")
		assert.Contains(t, valid, ref.Hash())
		reads++
	}
	wg.Wait()
	assert.Positive(t, reads)

	current, err := st.Reference(name)
	require.NoError(t, err)
	stale := plumbing.NewHashReference(name, plumbing.NewHash("3333333333333333333333333333333333333333"))
	err = st.CheckAndSetReference(plumbing.NewHashReference(name, hashes[0]), stale)
	assert.ErrorIs(t, err, storage.ErrReferenceHasChanged)
	require.NoError(t, st.CheckAndSetReference(plumbing.NewHashReference(name, hashes[0]), current))
	require.NoError(t, st.SetReference(plumbing.NewSymbolicReference("refs/heads/alias", name)))
	alias, err := st.Reference("refs/heads/alias")
	require.NoError(t, err)
	assert.Equal(t, name, alias.Target())

	iter, err := st.IterReferences()
	require.NoError(t, err)
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		assert.NotContains(t, r.Name().String(), "ogit-tmp", "temporary files are not refs")
		return nil
	})
}
