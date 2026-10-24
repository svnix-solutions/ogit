package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/revlist"
)

// Prune deletes objects that nothing points to any more (after a
// force-push or a branch deletion). An object is pruned only if:
//
//   - it is not reachable from HEAD, any ref, or any object in a pack
//     younger than expire. Young packs are pushes that may not have
//     updated their refs yet (and gc output), so whatever they point to
//     stays;
//   - every pack holding it is older than expire. Packs never change, so a
//     pack's upload time is a lower bound for the age of its objects.
//
// The old packs are rewritten as one pack with the objects they keep, then
// retired like gc's: a .drop marker hides them and a later run (of prune
// or gc) deletes them after grace. A missing object anywhere in the walk
// aborts the prune.

// PruneResult says what one prune did (or would do, in a dry run).
type PruneResult struct {
	Loose   int // loose objects packed first (counted as new)
	Packs   int // old packs rewritten
	Kept    int // objects of the old packs still reachable
	Pruned  int // objects removed
	Deleted int // packs deleted because their grace period was over
}

// Prune removes the repository's unreachable objects older than expire.
// With dryRun nothing is written or deleted.
func (s3s *S3Storage) Prune(repoPath string, expire, grace time.Duration, dryRun bool) (PruneResult, error) {
	var res PruneResult
	repoKey := s3s.getRepoKey(repoPath)
	var err error

	if !dryRun {
		if res.Deleted, err = s3s.deleteDroppedPacks(repoKey, grace); err != nil {
			return res, err
		}
		// Loose objects get a new pack, so they count as new: kept this time.
		if res.Loose, err = s3s.Repack(repoPath); err != nil {
			return res, err
		}
	}

	st := NewS3Storer(s3s.client, s3s.bucket, repoKey, s3s.Logger, s3s.cache)
	state, err := st.state()
	if err != nil {
		return res, err
	}
	cutoff := time.Now().Add(-expire)
	var old, young []*pack
	for _, p := range state.packs {
		if p.modified.After(cutoff) {
			young = append(young, p)
		} else {
			old = append(old, p)
		}
	}
	if len(old) == 0 {
		return res, nil
	}

	roots, err := pruneRoots(st, young)
	if err != nil {
		return res, err
	}
	reachable, err := revlist.Objects(st, roots, nil)
	if err != nil {
		return res, fmt.Errorf("walk reachable objects (nothing pruned): %w", err)
	}
	live := make(map[plumbing.Hash]bool, len(reachable))
	for _, h := range reachable {
		live[h] = true
	}

	seen := map[plumbing.Hash]bool{}
	var keep []plumbing.Hash
	pruned := 0
	for _, p := range old {
		if err := forEachHash(p, func(h plumbing.Hash) {
			if seen[h] {
				return
			}
			seen[h] = true
			if live[h] {
				keep = append(keep, h)
			} else {
				pruned++
			}
		}); err != nil {
			return res, err
		}
	}
	res.Kept, res.Pruned = len(keep), pruned
	if pruned == 0 {
		return res, nil // nothing to gain from rewriting
	}
	res.Packs = len(old)
	if dryRun {
		return res, nil
	}

	var merged *pack
	if len(keep) > 0 {
		if merged, err = s3s.writeVerifiedPack(st, repoKey, keep); err != nil {
			return res, err
		}
	}
	for _, p := range old {
		if merged != nil && p.key == merged.key {
			continue
		}
		if err := s3s.dropPack(p); err != nil {
			return res, err
		}
	}
	return res, nil
}

// pruneRoots returns what keeps objects alive: every ref (HEAD included)
// and every object in a young pack.
func pruneRoots(st *S3Storer, young []*pack) ([]plumbing.Hash, error) {
	var roots []plumbing.Hash
	refs, err := st.IterReferences()
	if err != nil {
		return nil, err
	}
	err = refs.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference {
			roots = append(roots, r.Hash())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if head, err := st.Reference(plumbing.HEAD); err == nil && head.Type() == plumbing.HashReference {
		roots = append(roots, head.Hash()) // detached HEAD
	}
	for _, p := range young {
		if err := forEachHash(p, func(h plumbing.Hash) { roots = append(roots, h) }); err != nil {
			return nil, err
		}
	}
	return roots, nil
}

func forEachHash(p *pack, fn func(plumbing.Hash)) error {
	entries, err := p.idx.Entries()
	if err != nil {
		return err
	}
	defer entries.Close()
	for {
		e, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		fn(e.Hash)
	}
}

// writeVerifiedPack encodes hashes (read through st) into a new pack and
// checks, with a fresh listing, that the new pack itself holds all of them.
func (s3s *S3Storage) writeVerifiedPack(st *S3Storer, repoKey string, hashes []plumbing.Hash) (*pack, error) {
	pw := &packWriter{s: st}
	var err error
	if pw.f, err = createTemp(); err != nil {
		return nil, err
	}
	if _, err := packfile.NewEncoder(pw, st, false).Encode(hashes, 10); err != nil {
		pw.Close()
		return nil, fmt.Errorf("encode pack: %w", err)
	}
	if err := pw.Close(); err != nil {
		return nil, err
	}

	check := NewS3Storer(s3s.client, s3s.bucket, repoKey, s3s.Logger, s3s.cache)
	fresh, err := check.state()
	if err != nil {
		return nil, err
	}
	for _, p := range fresh.packs {
		if p.key != pw.key {
			continue
		}
		for _, h := range hashes {
			if ok, _ := p.idx.Contains(h); !ok {
				return nil, fmt.Errorf("object %s missing from the new pack; old packs kept", h)
			}
		}
		return p, nil
	}
	return nil, fmt.Errorf("new pack %s is not listed; old packs kept", pw.key)
}

// dropPack hides p from new requests; deleteDroppedPacks deletes it later.
func (s3s *S3Storage) dropPack(p *pack) error {
	if _, err := s3s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s3s.bucket),
		Key:    aws.String(strings.TrimSuffix(p.key, ".pack") + dropSuffix),
		Body:   bytes.NewReader(nil),
	}); err != nil {
		return fmt.Errorf("drop %s: %w", p.key, err)
	}
	return nil
}
