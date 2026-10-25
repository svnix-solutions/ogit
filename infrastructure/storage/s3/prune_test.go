package s3_test

import (
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func storerOf(t *testing.T, storage *s3.S3Storage, repo string) *s3.S3Storer {
	t.Helper()
	raw, err := storage.GetStorer(repo)
	require.NoError(t, err)
	return raw.(*s3.S3Storer)
}

// pushTag writes an annotated tag on target, as its own pack, under refs/tags/name.
func pushTag(t *testing.T, st *s3.S3Storer, name string, target plumbing.Hash) plumbing.Hash {
	t.Helper()
	mem := memory.NewStorage()
	tag := &object.Tag{
		Name: name, Target: target, TargetType: plumbing.CommitObject, Message: "release\n",
		Tagger: object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(1, 0)},
	}
	obj := mem.NewEncodedObject()
	require.NoError(t, tag.Encode(obj))
	h, err := mem.SetEncodedObject(obj)
	require.NoError(t, err)
	pw, err := st.PackfileWriter()
	require.NoError(t, err)
	_, err = packfile.NewEncoder(pw, mem, false).Encode([]plumbing.Hash{h}, 10)
	require.NoError(t, err)
	require.NoError(t, pw.Close())
	require.NoError(t, st.SetReference(plumbing.NewHashReference(plumbing.NewTagReferenceName(name), h)))
	return h
}

// ageBoundary separates "old" packs written before it from "young" ones
// written after; prune with expire = time.Since(boundary) treats them so.
func ageBoundary() time.Time {
	time.Sleep(100 * time.Millisecond) // S3 times are in milliseconds; CI is slow
	b := time.Now()
	time.Sleep(100 * time.Millisecond)
	return b
}

func TestPrune(t *testing.T) { testPrune(t, s3test.StartFake(t)) }

func testPrune(t *testing.T, ep s3test.Endpoint) {
	storage := s3test.NewStorage(t, ep)
	require.NoError(t, storage.CreateRepository("r.git", "Test"))
	st := storerOf(t, storage, "r.git")
	ref, err := st.Reference("refs/heads/main")
	require.NoError(t, err)
	c1 := pushCommit(t, st, ref.Hash(), 1)
	c2 := pushCommit(t, st, c1, 2)
	c3 := pushCommit(t, st, c2, 3)
	f1 := pushCommitTo(t, st, "refs/heads/feature", c3, 10)
	tag := pushTag(t, st, "v1", c1)
	notes := pushCommitTo(t, st, "refs/notes/commits", plumbing.ZeroHash, 20)

	// A force-push drops c3, and deleting the branch drops f1.
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/main", c2)))
	require.NoError(t, st.RemoveReference("refs/heads/feature"))

	boundary := ageBoundary()
	// A push in flight: its pack is uploaded, its ref not updated yet, and
	// it builds on c3.
	inFlight := pushCommitTo(t, st, "", c3, 30)
	oldPacks := 7 // initial commit, c1, c2, c3, f1, tag, notes

	res, err := storage.Prune("r.git", time.Hour, time.Hour, false)
	require.NoError(t, err)
	assert.Equal(t, s3.PruneResult{}, res, "every pack is younger than the expiry")

	// Before: a request that is already reading the old packs.
	before := storerOf(t, storage, "r.git")
	_, err = before.VisiblePacksForTest()
	require.NoError(t, err)

	res, err = storage.Prune("r.git", time.Since(boundary), time.Hour, true)
	require.NoError(t, err)
	want := s3.PruneResult{Packs: oldPacks, Kept: 3*5 + 1, Pruned: 3} // 3 objects per commit pack, 1 for the tag
	assert.Equal(t, want, res, "dry run: only f1 (its blob, tree and commit) goes; c3 is needed by the push in flight")
	assert.Len(t, visiblePacks(t, storage, "r.git"), oldPacks+1, "dry run changes nothing")
	require.NoError(t, storerOf(t, storage, "r.git").HasEncodedObject(f1))

	res, err = storage.Prune("r.git", time.Since(boundary), time.Hour, false)
	require.NoError(t, err)
	assert.Equal(t, want, res)

	after := storerOf(t, storage, "r.git")
	assert.ErrorIs(t, after.HasEncodedObject(f1), plumbing.ErrObjectNotFound, "the deleted branch's commit is gone")
	for name, h := range map[string]plumbing.Hash{"c3": c3, "in flight": inFlight, "tag": tag, "notes": notes, "c1": c1} {
		assert.NoError(t, after.HasEncodedObject(h), name)
	}
	assert.Equal(t, 3, readHistory(t, storage, "r.git"), "main: initial commit, c1, c2")
	_, err = object.GetTag(after, tag)
	assert.NoError(t, err)
	assert.Len(t, visiblePacks(t, storage, "r.git"), 2, "one rewritten pack and the young one")
	assert.NoError(t, before.HasEncodedObject(f1), "an earlier request still reads the old packs")

	res, err = storage.Prune("r.git", time.Since(boundary), 0, false)
	require.NoError(t, err)
	assert.Equal(t, oldPacks, res.Deleted, "the next run deletes the replaced packs")
	assert.Zero(t, res.Pruned, "nothing else to prune")
	assert.Len(t, packKeys(t, ep, "r.git", ".pack"), 2)
	assert.Equal(t, 3, readHistory(t, storage, "r.git"))
}

// A ref to a missing object means the walk can't be trusted: prune
// nothing.
func TestPruneAbortsOnMissingObject(t *testing.T) {
	storage := s3test.NewStorage(t, s3test.StartFake(t))
	newRepoWithPushes(t, storage, "r.git", 2)
	st := storerOf(t, storage, "r.git")
	ref, err := st.Reference("refs/heads/main")
	require.NoError(t, err)
	pushCommitTo(t, st, "refs/heads/dropped", ref.Hash(), 9)
	require.NoError(t, st.RemoveReference("refs/heads/dropped"))
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/heads/broken", plumbing.NewHash("1234567890123456789012345678901234567890"))))
	boundary := ageBoundary()

	_, err = storage.Prune("r.git", time.Since(boundary), 0, false)
	require.ErrorContains(t, err, "nothing pruned")
	assert.Len(t, visiblePacks(t, storage, "r.git"), 4, "no pack was dropped")
}
