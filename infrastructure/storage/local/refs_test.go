package local

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
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
	require.NoError(t, ls.CreateRepository("r", "Test"))
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

// A storer that already listed the packs still finds objects in a pack
// written later by another storer (another request's push or merge).
func TestObjectsInPacksWrittenByOthersAreFound(t *testing.T) {
	var cfg config.Config
	cfg.Storage.Local.Path = filepath.Join(t.TempDir(), "repos")
	ls := NewLocalStorage(zerolog.Nop(), cfg)
	require.NoError(t, ls.Configure())
	require.NoError(t, ls.CreateRepository("r", "Test"))
	reader, err := ls.GetStorer("r")
	require.NoError(t, err)
	head, err := reader.Reference(plumbing.NewBranchReferenceName("main"))
	require.NoError(t, err)
	_, err = object.GetCommit(reader, head.Hash()) // lists the packs
	require.NoError(t, err)

	// Another storer writes a commit as a pack.
	mem := memory.NewStorage()
	c := &object.Commit{Message: "later", TreeHash: plumbing.ZeroHash, ParentHashes: []plumbing.Hash{head.Hash()},
		Author: object.Signature{Name: "a", Email: "a@x"}, Committer: object.Signature{Name: "a", Email: "a@x"}}
	enc := mem.NewEncodedObject()
	require.NoError(t, c.Encode(enc))
	h, err := mem.SetEncodedObject(enc)
	require.NoError(t, err)
	writer, err := ls.GetStorer("r")
	require.NoError(t, err)
	pw, err := writer.(storer.PackfileWriter).PackfileWriter()
	require.NoError(t, err)
	_, err = packfile.NewEncoder(pw, mem, false).Encode([]plumbing.Hash{h}, 10)
	require.NoError(t, err)
	require.NoError(t, pw.Close())

	got, err := object.GetCommit(reader, h)
	require.NoError(t, err, "the reader lists the packs again")
	assert.Equal(t, "later", got.Message)
	assert.NoError(t, reader.HasEncodedObject(h))
}
