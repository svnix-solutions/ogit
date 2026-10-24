package s3_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeSplit(t *testing.T) {
	for _, tc := range []struct {
		counts []int64
		want   int
	}{
		{nil, 0},
		{[]int64{5}, 0},
		{[]int64{1, 100}, 0},              // already geometric
		{[]int64{1, 2, 4, 8}, 0},          // exactly factor 2
		{[]int64{3, 3}, 2},                // equal packs merge
		{[]int64{3, 3, 3, 3, 3, 3, 3}, 7}, // many pushes: all of them
		{[]int64{3, 3, 27}, 2},            // the big pack stays
		{[]int64{3, 3, 3, 3, 3, 27}, 6},   // unless the merged pack would reach half of it
		{[]int64{1, 1, 10, 1000}, 2},      // only the tail breaks the progression
		{[]int64{1, 5, 6, 1000}, 3},       // 5 and 6 break it; 1 is absorbed too
	} {
		assert.Equal(t, tc.want, s3.MergeSplitForTest(tc.counts), "%v", tc.counts)
	}
}

// pushCommit writes one commit (a blob, a tree and the commit) as its own
// pack, like a push, and moves main to it.
func pushCommit(t *testing.T, st *s3.S3Storer, parent plumbing.Hash, n int) plumbing.Hash {
	t.Helper()
	return pushCommitTo(t, st, "refs/heads/main", parent, n)
}

// pushCommitTo is pushCommit onto ref; an empty ref leaves the refs alone,
// like a push whose pack is uploaded but whose ref update has not happened.
func pushCommitTo(t *testing.T, st *s3.S3Storer, ref plumbing.ReferenceName, parent plumbing.Hash, n int) plumbing.Hash {
	t.Helper()
	mem := memory.NewStorage()
	put := func(o interface {
		Encode(plumbing.EncodedObject) error
	}) plumbing.Hash {
		obj := mem.NewEncodedObject()
		require.NoError(t, o.Encode(obj))
		h, err := mem.SetEncodedObject(obj)
		require.NoError(t, err)
		return h
	}
	blob := mem.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, _ := blob.Writer()
	_, _ = fmt.Fprintf(w, "content %d\n", n)
	_ = w.Close()
	blobHash, err := mem.SetEncodedObject(blob)
	require.NoError(t, err)
	tree := put(&object.Tree{Entries: []object.TreeEntry{{Name: "f.txt", Mode: filemode.Regular, Hash: blobHash}}})
	sig := object.Signature{Name: "t", Email: "t@example.com", When: time.Unix(int64(n), 0)}
	commit := &object.Commit{Author: sig, Committer: sig, Message: fmt.Sprintf("c%d\n", n), TreeHash: tree}
	if !parent.IsZero() {
		commit.ParentHashes = []plumbing.Hash{parent}
	}
	h := put(commit)

	pw, err := st.PackfileWriter()
	require.NoError(t, err)
	_, err = packfile.NewEncoder(pw, mem, false).Encode([]plumbing.Hash{blobHash, tree, h}, 10)
	require.NoError(t, err)
	require.NoError(t, pw.Close())
	if ref != "" {
		require.NoError(t, st.SetReference(plumbing.NewHashReference(ref, h)))
	}
	return h
}

// readHistory walks main back to the root and reads every file.
func readHistory(t *testing.T, storage *s3.S3Storage, repo string) int {
	t.Helper()
	st, err := storage.GetStorer(repo)
	require.NoError(t, err)
	ref, err := st.Reference("refs/heads/main")
	require.NoError(t, err)
	n := 0
	iter := object.NewCommitPreorderIter(mustCommit(t, st, ref.Hash()), nil, nil)
	require.NoError(t, iter.ForEach(func(c *object.Commit) error {
		n++
		files, err := c.Files()
		if err != nil {
			return err
		}
		return files.ForEach(func(f *object.File) error { _, err := f.Contents(); return err })
	}))
	return n
}

func mustCommit(t *testing.T, st storer.EncodedObjectStorer, h plumbing.Hash) *object.Commit {
	t.Helper()
	c, err := object.GetCommit(st, h)
	require.NoError(t, err)
	return c
}

func packKeys(t *testing.T, ep s3test.Endpoint, repo, suffix string) []string {
	var out []string
	for _, k := range s3test.Keys(t, ep, "repositories/"+repo+"/objects/pack/") {
		if strings.HasSuffix(k, suffix) {
			out = append(out, k)
		}
	}
	return out
}

func newRepoWithPushes(t *testing.T, storage *s3.S3Storage, repo string, pushes int) {
	t.Helper()
	require.NoError(t, storage.CreateRepository(repo))
	raw, err := storage.GetStorer(repo)
	require.NoError(t, err)
	st := raw.(*s3.S3Storer)
	ref, err := st.Reference("refs/heads/main")
	require.NoError(t, err)
	head := ref.Hash()
	for i := 1; i <= pushes; i++ {
		head = pushCommit(t, st, head, i)
	}
}

func visiblePacks(t *testing.T, storage *s3.S3Storage, repo string) []string {
	t.Helper()
	st, err := storage.GetStorer(repo)
	require.NoError(t, err)
	keys, err := st.(*s3.S3Storer).VisiblePacksForTest()
	require.NoError(t, err)
	return keys
}

func TestGCMergesSmallPacks(t *testing.T) { testGCMergesSmallPacks(t, s3test.StartFake(t)) }

func testGCMergesSmallPacks(t *testing.T, ep s3test.Endpoint) {
	storage := s3test.NewStorage(t, ep)
	newRepoWithPushes(t, storage, "r.git", 8)
	require.Len(t, visiblePacks(t, storage, "r.git"), 9, "the initial commit and 8 pushes")

	// A request that listed the packs before GC.
	before, err := storage.GetStorer("r.git")
	require.NoError(t, err)
	_, err = before.(*s3.S3Storer).VisiblePacksForTest()
	require.NoError(t, err)

	res, err := storage.GC("r.git", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, s3.GCResult{Merged: 9, Objects: 27}, res)
	assert.Len(t, visiblePacks(t, storage, "r.git"), 1, "new requests read one pack")
	assert.Equal(t, 9, readHistory(t, storage, "r.git"))

	assert.Len(t, packKeys(t, ep, "r.git", ".pack"), 10, "old packs are kept during the grace period")
	assert.Len(t, packKeys(t, ep, "r.git", ".drop"), 9)
	ref, err := before.Reference("refs/heads/main")
	require.NoError(t, err)
	mustCommit(t, before, ref.Hash()) // the earlier request still reads its packs

	res, err = storage.GC("r.git", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, s3.GCResult{}, res, "nothing to merge, grace not over")

	res, err = storage.GC("r.git", 0)
	require.NoError(t, err)
	assert.Equal(t, s3.GCResult{Deleted: 9}, res)
	assert.Len(t, packKeys(t, ep, "r.git", ".pack"), 1)
	assert.Len(t, packKeys(t, ep, "r.git", ".idx"), 1)
	assert.Empty(t, packKeys(t, ep, "r.git", ".drop"))
	assert.Equal(t, 9, readHistory(t, storage, "r.git"))

	// Two more pushes: they are merged together, the big pack is left alone.
	big := visiblePacks(t, storage, "r.git")[0]
	raw, err := storage.GetStorer("r.git")
	require.NoError(t, err)
	st := raw.(*s3.S3Storer)
	ref, err = st.Reference("refs/heads/main")
	require.NoError(t, err)
	head := pushCommit(t, st, ref.Hash(), 9)
	pushCommit(t, st, head, 10)
	res, err = storage.GC("r.git", 0)
	require.NoError(t, err)
	assert.Equal(t, s3.GCResult{Merged: 2, Objects: 6}, res)
	assert.Len(t, visiblePacks(t, storage, "r.git"), 2)
	assert.Contains(t, visiblePacks(t, storage, "r.git"), big)
	assert.Equal(t, 11, readHistory(t, storage, "r.git"))
}

// A pack whose index is not uploaded yet is a push in progress, whatever
// the grace period.
func TestGCKeepsPacksBeingUploaded(t *testing.T) {
	ep := s3test.StartFake(t)
	storage := s3test.NewStorage(t, ep)
	newRepoWithPushes(t, storage, "r.git", 2)
	idx := packKeys(t, ep, "r.git", ".idx")
	require.Len(t, idx, 3)
	s3test.DeleteKey(t, ep, idx[0])

	res, err := storage.GC("r.git", 0)
	require.NoError(t, err)
	assert.Zero(t, res.Deleted)
	assert.Len(t, packKeys(t, ep, "r.git", ".pack"), 4, "3 packs and the merge of the 2 indexed ones")
}

func TestGCPacksLooseObjectsToo(t *testing.T) {
	ep := s3test.StartFake(t)
	storage := s3test.NewStorage(t, ep)
	s3test.CreateLegacyRepository(t, ep, "old", map[string]string{"README.md": "x"})
	res, err := storage.GC("old.git", time.Hour)
	require.NoError(t, err)
	assert.Equal(t, s3.GCResult{Loose: 3}, res)
	assert.Equal(t, 1, readHistory(t, storage, "old.git"))
}

// Two GCs at once (two servers or two cron jobs) must not hide each
// other's merged packs.
func TestConcurrentGC(t *testing.T) {
	ep := s3test.StartFake(t)
	storage := s3test.NewStorage(t, ep)
	other := s3test.NewStorage(t, ep) // a second server: its own cache
	newRepoWithPushes(t, storage, "r.git", 6)

	for round := 0; round < 4; round++ {
		raw, err := storage.GetStorer("r.git")
		require.NoError(t, err)
		st := raw.(*s3.S3Storer)
		ref, err := st.Reference("refs/heads/main")
		require.NoError(t, err)
		head := ref.Hash()
		for i := 0; i < 3; i++ {
			head = pushCommit(t, st, head, 100*round+i)
		}

		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, s := range []*s3.S3Storage{storage, other} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = s.GC("r.git", time.Hour)
			}()
		}
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
		assert.Equal(t, 7+3*(round+1), readHistory(t, storage, "r.git"), "round %d", round)
		assert.Equal(t, 7+3*(round+1), readHistory(t, other, "r.git"), "round %d", round)
	}
	_, err := storage.GC("r.git", 0)
	require.NoError(t, err)
	assert.Equal(t, 19, readHistory(t, other, "r.git"))
}
