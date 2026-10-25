//go:build integration

package s3_test

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With several servers on one bucket, a ref update must fail if another
// server changed the ref after it was read. This needs real conditional
// writes, so it runs against MinIO (gofakes3 ignores If-Match).
func TestS3ConditionalRefWrites_MinIO(t *testing.T) {
	st := s3test.NewStorage(t, s3test.StartMinIO(t))
	require.NoError(t, st.CreateRepository("race.git", "Test"))
	a, err := st.GetStorer("race.git")
	require.NoError(t, err)
	b, err := st.GetStorer("race.git")
	require.NoError(t, err)
	serverA, serverB := a.(*s3.S3Storer), b.(*s3.S3Storer)

	main := plumbing.ReferenceName("refs/heads/main")
	x := plumbing.NewHash("1111111111111111111111111111111111111111")
	y := plumbing.NewHash("2222222222222222222222222222222222222222")

	t.Run("update_after_concurrent_change_fails", func(t *testing.T) {
		_, etag, err := serverA.ReadRefForTest(main)
		require.NoError(t, err)
		require.NoError(t, serverB.SetReference(plumbing.NewHashReference(main, x)), "another server pushes")
		err = serverA.PutRefIfMatchForTest(plumbing.NewHashReference(main, y), etag)
		assert.ErrorIs(t, err, storage.ErrReferenceHasChanged)
		ref, err := serverA.Reference(main)
		require.NoError(t, err)
		assert.Equal(t, x, ref.Hash(), "the other server's update is kept")
	})

	t.Run("check_and_set", func(t *testing.T) {
		assert.ErrorIs(t, serverA.CheckAndSetReference(plumbing.NewHashReference(main, y), plumbing.NewHashReference(main, y)), storage.ErrReferenceHasChanged)
		require.NoError(t, serverA.CheckAndSetReference(plumbing.NewHashReference(main, y), plumbing.NewHashReference(main, x)))
	})

	t.Run("create_only_if_missing", func(t *testing.T) {
		feature := plumbing.NewHashReference("refs/heads/feature", x)
		require.NoError(t, serverA.CreateReference(feature))
		assert.ErrorIs(t, serverB.CreateReference(feature), storage.ErrReferenceHasChanged, "a concurrent create loses")
	})

	// Deletes compare the stored value first. The DELETE itself is also
	// conditional (If-Match), but only some providers enforce that (AWS S3
	// does, MinIO does not), so only the comparison is guaranteed here.
	t.Run("delete_checks_the_old_value", func(t *testing.T) {
		require.NoError(t, serverB.SetReference(plumbing.NewHashReference("refs/heads/feature", y)))
		assert.ErrorIs(t, serverA.RemoveReferenceIf("refs/heads/feature", x), storage.ErrReferenceHasChanged)
		require.NoError(t, serverA.RemoveReferenceIf("refs/heads/feature", y))
		_, err := serverA.Reference("refs/heads/feature")
		assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound)
	})
}
