package git

import (
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commit stores a commit with one file on top of parent and returns it.
func commit(t *testing.T, st storer.EncodedObjectStorer, parent plumbing.Hash, name, content string) plumbing.Hash {
	t.Helper()
	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, _ := blob.Writer()
	_, _ = w.Write([]byte(content))
	_ = w.Close()
	bh, err := st.SetEncodedObject(blob)
	require.NoError(t, err)
	tree := &object.Tree{Entries: []object.TreeEntry{{Name: name, Mode: 0o100644, Hash: bh}}}
	to := st.NewEncodedObject()
	require.NoError(t, tree.Encode(to))
	th, err := st.SetEncodedObject(to)
	require.NoError(t, err)
	sig := object.Signature{Name: "a", Email: "a@x", When: time.Unix(1700000000, 0)}
	c := &object.Commit{Author: sig, Committer: sig, Message: name, TreeHash: th}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}
	co := st.NewEncodedObject()
	require.NoError(t, c.Encode(co))
	h, err := st.SetEncodedObject(co)
	require.NoError(t, err)
	return h
}

func TestCopyObjects(t *testing.T) {
	src := memory.NewStorage()
	c1 := commit(t, src, plumbing.ZeroHash, "a.txt", "a")
	c2 := commit(t, src, c1, "b.txt", "b")
	dst := filesystem.NewStorage(osfs.New(t.TempDir()), cache.NewObjectLRUDefault())

	n, err := CopyObjects(src, dst, []plumbing.Hash{c1}, nil)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "commit, tree, blob")
	_, err = object.GetCommit(dst, c1)
	require.NoError(t, err)

	n, err = CopyObjects(src, dst, []plumbing.Hash{c2}, []plumbing.Hash{c1, plumbing.NewHash("1234567812345678123456781234567812345678")})
	require.NoError(t, err)
	assert.Equal(t, 3, n, "only what c1 doesn't have; unknown haves are ignored")
	got, err := object.GetCommit(dst, c2)
	require.NoError(t, err)
	parent, err := got.Parent(0)
	require.NoError(t, err, "history reachable")
	_, err = parent.File("a.txt")
	assert.NoError(t, err)

	n, err = CopyObjects(src, dst, []plumbing.Hash{c2}, []plumbing.Hash{c2})
	require.NoError(t, err)
	assert.Zero(t, n, "nothing new")
}

func TestOverlay(t *testing.T) {
	target, fork := memory.NewStorage(), memory.NewStorage()
	base := commit(t, target, plumbing.ZeroHash, "a.txt", "a")
	_, err := CopyObjectsMemory(target, fork, base)
	require.NoError(t, err)
	head := commit(t, fork, base, "b.txt", "b")

	o := Overlay{Primary: target, Secondary: fork}
	c, err := object.GetCommit(o, head)
	require.NoError(t, err)
	parent, err := c.Parent(0)
	require.NoError(t, err)
	assert.Equal(t, base, parent.Hash)
	_, err = o.SetEncodedObject(o.NewEncodedObject())
	assert.Error(t, err, "read only")
	_, err = object.GetCommit(target, head)
	assert.Error(t, err, "nothing was written to the target")
}

// CopyObjectsMemory copies everything reachable from tip between memory
// storages (test setup).
func CopyObjectsMemory(src, dst *memory.Storage, tip plumbing.Hash) (int, error) {
	n := 0
	iter, err := src.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return 0, err
	}
	err = iter.ForEach(func(o plumbing.EncodedObject) error {
		n++
		_, err := dst.SetEncodedObject(o)
		return err
	})
	return n, err
}
