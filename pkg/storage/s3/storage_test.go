package s3_test

import (
	"io"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/labbs/git-server-s3/internal/testutil/s3test"
	"github.com/labbs/git-server-s3/pkg/storage/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3Storage_Fake runs the storage suite against an in-memory S3.
// The same suite runs against MinIO in storage_integration_test.go.
func TestS3Storage_Fake(t *testing.T) {
	testStorageSuite(t, s3test.NewStorage(t, s3test.StartFake(t)))
}

func testStorageSuite(t *testing.T, storage *s3.S3Storage) {
	t.Run("list_empty", func(t *testing.T) {
		repos, err := storage.ListRepositories()
		require.NoError(t, err)
		assert.Empty(t, repos)
	})

	t.Run("create_and_exists", func(t *testing.T) {
		assert.False(t, storage.RepositoryExists("alpha.git"))
		require.NoError(t, storage.CreateRepository("alpha.git"))
		assert.True(t, storage.RepositoryExists("alpha.git"))
		// getRepoKey appends .git, so both spellings refer to the same repo
		assert.True(t, storage.RepositoryExists("alpha"))
	})

	t.Run("create_duplicate_fails", func(t *testing.T) {
		assert.Error(t, storage.CreateRepository("alpha.git"))
	})

	t.Run("initial_commit_is_readable", func(t *testing.T) {
		st, err := storage.GetStorer("alpha.git")
		require.NoError(t, err)

		head, err := st.Reference(plumbing.HEAD)
		require.NoError(t, err)
		assert.Equal(t, plumbing.SymbolicReference, head.Type())
		assert.Equal(t, plumbing.ReferenceName("refs/heads/main"), head.Target())

		main, err := st.Reference("refs/heads/main")
		require.NoError(t, err)

		commit, err := object.GetCommit(st, main.Hash())
		require.NoError(t, err)
		assert.Empty(t, commit.ParentHashes)

		tree, err := commit.Tree()
		require.NoError(t, err)
		file, err := tree.File("README.md")
		require.NoError(t, err)
		content, err := file.Contents()
		require.NoError(t, err)
		assert.Contains(t, content, "# Repository")
	})

	t.Run("iter_references", func(t *testing.T) {
		st, err := storage.GetStorer("alpha.git")
		require.NoError(t, err)

		iter, err := st.IterReferences()
		require.NoError(t, err)
		var names []plumbing.ReferenceName
		require.NoError(t, iter.ForEach(func(r *plumbing.Reference) error {
			names = append(names, r.Name())
			return nil
		}))
		assert.ElementsMatch(t, []plumbing.ReferenceName{plumbing.HEAD, "refs/heads/main"}, names)
	})

	t.Run("object_roundtrip", func(t *testing.T) {
		st, err := storage.GetStorer("alpha.git")
		require.NoError(t, err)

		obj := st.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		require.NoError(t, err)
		_, err = w.Write([]byte("hello s3"))
		require.NoError(t, err)
		require.NoError(t, w.Close())

		hash, err := st.SetEncodedObject(obj)
		require.NoError(t, err)
		assert.Equal(t, obj.Hash(), hash)
		require.NoError(t, st.HasEncodedObject(hash))

		size, err := st.EncodedObjectSize(hash)
		require.NoError(t, err)
		assert.EqualValues(t, len("hello s3"), size)

		got, err := st.EncodedObject(plumbing.AnyObject, hash)
		require.NoError(t, err)
		assert.Equal(t, plumbing.BlobObject, got.Type())
		r, err := got.Reader()
		require.NoError(t, err)
		data, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, "hello s3", string(data))

		require.NoError(t, st.(*s3.S3Storer).DeleteEncodedObject(hash))
		assert.ErrorIs(t, st.HasEncodedObject(hash), plumbing.ErrObjectNotFound)
	})

	t.Run("check_and_set_reference", func(t *testing.T) {
		st, err := storage.GetStorer("alpha.git")
		require.NoError(t, err)

		main, err := st.Reference("refs/heads/main")
		require.NoError(t, err)

		other := plumbing.NewHash("1111111111111111111111111111111111111111")
		stale := plumbing.NewHashReference("refs/heads/main", other)
		assert.Error(t, st.CheckAndSetReference(plumbing.NewHashReference("refs/heads/main", other), stale))

		require.NoError(t, st.CheckAndSetReference(plumbing.NewHashReference("refs/heads/feature", main.Hash()), nil))
		feature, err := st.Reference("refs/heads/feature")
		require.NoError(t, err)
		assert.Equal(t, main.Hash(), feature.Hash())

		require.NoError(t, st.RemoveReference("refs/heads/feature"))
		_, err = st.Reference("refs/heads/feature")
		assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound)
	})

	t.Run("list_multiple", func(t *testing.T) {
		require.NoError(t, storage.CreateRepository("beta"))
		repos, err := storage.ListRepositories()
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"alpha.git", "beta.git"}, repos)
	})

	t.Run("get_storer_missing_repo", func(t *testing.T) {
		_, err := storage.GetStorer("missing.git")
		assert.Error(t, err)
	})

	t.Run("delete", func(t *testing.T) {
		require.NoError(t, storage.DeleteRepository("beta.git"))
		assert.False(t, storage.RepositoryExists("beta.git"))
		assert.Error(t, storage.DeleteRepository("beta.git"))

		repos, err := storage.ListRepositories()
		require.NoError(t, err)
		assert.Equal(t, []string{"alpha.git"}, repos)
	})
}
