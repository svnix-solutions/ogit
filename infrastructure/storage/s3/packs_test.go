package s3_test

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFile(t *testing.T, st storer.EncodedObjectStorer, commit plumbing.Hash, name string) string {
	t.Helper()
	c, err := object.GetCommit(st, commit)
	require.NoError(t, err)
	tree, err := c.Tree()
	require.NoError(t, err)
	f, err := tree.File(name)
	require.NoError(t, err)
	content, err := f.Contents()
	require.NoError(t, err)
	return content
}

func TestNewRepositoriesArePacked(t *testing.T) {
	st := s3test.NewStorage(t, s3test.StartFake(t))
	require.NoError(t, st.CreateRepository("new.git"))
	n, err := st.Repack("new.git")
	require.NoError(t, err)
	assert.Zero(t, n, "the initial commit is written as a pack: nothing loose to repack")
}

// Repositories written before packs keep working, and repack converts them.
func TestLegacyLooseRepository(t *testing.T) {
	ep := s3test.StartFake(t)
	st := s3test.NewStorage(t, ep)
	commit := s3test.CreateLegacyRepository(t, ep, "old", map[string]string{"README.md": "hello\n", "a.txt": "a"})

	legacy, err := st.GetStorer("old.git")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", readFile(t, legacy, commit, "README.md"), "loose objects are read as a fallback")

	n, err := st.Repack("old.git")
	require.NoError(t, err)
	assert.Equal(t, 4, n, "two blobs, a tree and a commit")

	packed, err := st.GetStorer("old.git")
	require.NoError(t, err)
	assert.Equal(t, "hello\n", readFile(t, packed, commit, "README.md"), "readable from the pack")
	assert.Equal(t, "a", readFile(t, packed, commit, "a.txt"))
	n, err = st.Repack("old.git")
	require.NoError(t, err)
	assert.Zero(t, n, "nothing loose is left")

	// A loose object written next to packs (e.g. by an old server) is read
	// too, and a later repack picks it up.
	blob := &plumbing.MemoryObject{}
	blob.SetType(plumbing.BlobObject)
	blob.Write([]byte("loose again"))
	h, err := packed.SetEncodedObject(blob)
	require.NoError(t, err)
	require.NoError(t, packed.HasEncodedObject(h))
	fresh, err := st.GetStorer("old.git")
	require.NoError(t, err)
	require.NoError(t, fresh.HasEncodedObject(h), "found by a new request too")
	n, err = st.Repack("old.git")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	fresh, err = st.GetStorer("old.git")
	require.NoError(t, err)
	obj, err := fresh.EncodedObject(plumbing.BlobObject, h)
	require.NoError(t, err)
	assert.Equal(t, plumbing.BlobObject, obj.Type())
	_, ok := fresh.(*s3.S3Storer)
	assert.True(t, ok)
}
