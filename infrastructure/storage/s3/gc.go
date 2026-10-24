package s3

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-git/go-git/v5/plumbing"
)

// Every push adds a pack, and every request lists the packs and loads their
// indexes, so a busy repository gets slower with each push. GC merges the
// small packs, like `git repack --geometric=2`: packs are merged until each
// remaining one has at least twice the objects of the next smaller one, so
// big old packs are left alone and a merge costs about the size of recent
// pushes. Objects are only moved, never pruned, so GC cannot lose data even
// while pushes are running.
//
// Requests that started before a merge may still read the old packs, so
// they are not deleted right away: GC writes pack-<sha>.drop next to them,
// which hides them from new requests (state() skips them), and a later GC
// deletes them once that marker is older than the grace period.
//
// Two GCs on the same repository need no lock: each one hides packs only
// after its own merged pack is uploaded and verified, so every hidden object
// is in a complete, visible pack. At worst they do the work twice.

const (
	dropSuffix      = ".drop"
	geometricFactor = 2

	// unindexedAge is when a pack without an index counts as a failed
	// upload. It does not depend on --grace: a push being uploaded right now
	// also has its .pack before its .idx.
	unindexedAge = 24 * time.Hour
)

// GCResult says what one GC run did.
type GCResult struct {
	Loose   int // loose objects packed first (see Repack)
	Merged  int // packs merged into a new one
	Objects int // objects in the new pack
	Deleted int // packs deleted because their grace period was over
}

// GC merges the repository's small packs and deletes the packs that an
// earlier run replaced more than grace ago.
func (s3s *S3Storage) GC(repoPath string, grace time.Duration) (GCResult, error) {
	var res GCResult
	repoKey := s3s.getRepoKey(repoPath)

	// Delete first, from what earlier runs dropped: packs this run drops get
	// their full grace period.
	deleted, err := s3s.deleteDroppedPacks(repoKey, grace)
	if err != nil {
		return res, err
	}
	res.Deleted = deleted

	if res.Loose, err = s3s.Repack(repoPath); err != nil {
		return res, err
	}

	st := NewS3Storer(s3s.client, s3s.bucket, repoKey, s3s.Logger, s3s.cache)
	state, err := st.state()
	if err != nil {
		return res, err
	}
	merge := packsToMerge(state.packs)
	if len(merge) < 2 {
		return res, nil
	}

	seen := map[plumbing.Hash]bool{}
	var hashes []plumbing.Hash
	for _, p := range merge {
		if err := forEachHash(p, func(h plumbing.Hash) {
			if !seen[h] {
				seen[h] = true
				hashes = append(hashes, h)
			}
		}); err != nil {
			return res, err
		}
	}

	// Encoded through the storer: it reads the objects from the old packs.
	merged, err := s3s.writeVerifiedPack(st, repoKey, hashes)
	if err != nil {
		return res, err
	}
	for _, p := range merge {
		if p.key == merged.key {
			continue // same objects merged again: identical pack
		}
		if err := s3s.dropPack(p); err != nil {
			return res, err
		}
	}
	res.Merged = len(merge)
	res.Objects = len(hashes)
	return res, nil
}

// packsToMerge returns the packs to merge, smallest first; none if the
// packs already form a geometric progression.
func packsToMerge(packs []*pack) []*pack {
	count := func(p *pack) int64 { n, _ := p.idx.Count(); return n }
	sorted := append([]*pack(nil), packs...)
	sort.SliceStable(sorted, func(i, j int) bool { return count(sorted[i]) < count(sorted[j]) })
	counts := make([]int64, len(sorted))
	for i, p := range sorted {
		counts[i] = count(p)
	}
	return sorted[:mergeSplit(counts)]
}

// mergeSplit takes object counts in ascending order and returns how many of
// the smallest to merge so that what is left, merged pack included, grows by
// at least geometricFactor from one pack to the next. It returns 0 when
// nothing needs merging.
func mergeSplit(counts []int64) int {
	// The largest packs that already progress geometrically stay.
	split := 0
	for i := len(counts) - 1; i > 0; i-- {
		if counts[i-1]*geometricFactor > counts[i] {
			split = i
			break
		}
	}
	// The merged pack may itself break the progression: absorb the next
	// packs until it doesn't.
	var merged int64
	for _, n := range counts[:split] {
		merged += n
	}
	for split < len(counts) && counts[split] < geometricFactor*merged {
		merged += counts[split]
		split++
	}
	if split < 2 {
		return 0
	}
	return split
}

// deleteDroppedPacks deletes the packs whose .drop marker is older than
// grace, and packs left without an index (a failed upload) for longer than
// unindexedAge. The marker goes last, so an interrupted run is finished next time.
func (s3s *S3Storage) deleteDroppedPacks(repoKey string, grace time.Duration) (int, error) {
	ctx := context.TODO()
	prefix := repoKey + "/objects/pack/"
	modified := map[string]time.Time{}
	pager := awss3.NewListObjectsV2Paginator(s3s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s3s.bucket),
		Prefix: aws.String(prefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return 0, err
		}
		for _, o := range page.Contents {
			modified[aws.ToString(o.Key)] = aws.ToTime(o.LastModified)
		}
	}

	now := time.Now()
	cutoff := now.Add(-grace)
	var keys []string
	deleted := 0
	for key, t := range modified {
		if !strings.HasSuffix(key, ".pack") {
			continue
		}
		base := strings.TrimSuffix(key, ".pack")
		_, hasIdx := modified[base+".idx"]
		drop, hasDrop := modified[base+dropSuffix]
		switch {
		case hasDrop && !drop.After(cutoff):
		case !hasDrop && !hasIdx && !t.After(now.Add(-unindexedAge)):
		default:
			continue
		}
		deleted++
		keys = append(keys, base+".idx", key)
	}
	// Markers whose pack is gone (an interrupted delete) go too.
	var markers []string
	for key, t := range modified {
		if strings.HasSuffix(key, dropSuffix) && !t.After(cutoff) {
			markers = append(markers, key)
		}
	}
	for _, batch := range [][]string{keys, markers} {
		if err := s3s.deleteKeys(ctx, batch); err != nil {
			return 0, err
		}
	}
	return deleted, nil
}

func (s3s *S3Storage) deleteKeys(ctx context.Context, keys []string) error {
	for start := 0; start < len(keys); start += 1000 {
		var objects []types.ObjectIdentifier
		for _, k := range keys[start:min(start+1000, len(keys))] {
			objects = append(objects, types.ObjectIdentifier{Key: aws.String(k)})
		}
		if _, err := s3s.client.DeleteObjects(ctx, &awss3.DeleteObjectsInput{
			Bucket: aws.String(s3s.bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return fmt.Errorf("delete old packs: %w", err)
		}
	}
	return nil
}
