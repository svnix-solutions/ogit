package s3

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// Repack converts a repository's loose objects (the layout used before
// packs) into one pack, then deletes them. It returns how many objects it
// packed (0 if there were none). Loose objects are only deleted once every
// one of them is found in a pack, so an interrupted repack leaves the
// repository readable.
func (s3s *S3Storage) Repack(repoPath string) (int, error) {
	repoKey := s3s.getRepoKey(repoPath)
	st := NewS3Storer(s3s.client, s3s.bucket, repoKey, s3s.Logger, s3s.cache)
	state, err := st.state()
	if err != nil {
		return 0, err
	}
	if !state.hasLoose {
		return 0, nil
	}

	hashes, keys, err := st.looseObjects()
	if err != nil {
		return 0, err
	}
	if len(hashes) == 0 {
		return 0, nil
	}

	// Encode through the storer itself: it reads the loose objects.
	pw, err := st.PackfileWriter()
	if err != nil {
		return 0, err
	}
	if _, err := packfile.NewEncoder(pw, st, false).Encode(hashes, 10); err != nil {
		pw.Close()
		return 0, fmt.Errorf("encode pack: %w", err)
	}
	if err := pw.Close(); err != nil {
		return 0, err
	}

	// Verify with a fresh listing before deleting anything.
	check := NewS3Storer(s3s.client, s3s.bucket, repoKey, s3s.Logger, s3s.cache)
	for _, h := range hashes {
		if _, ok, err := check.findInPacks(h); err != nil {
			return 0, err
		} else if !ok {
			return 0, fmt.Errorf("object %s missing from the new pack; loose objects kept", h)
		}
	}

	for start := 0; start < len(keys); start += 1000 {
		end := min(start+1000, len(keys))
		var objects []types.ObjectIdentifier
		for _, k := range keys[start:end] {
			objects = append(objects, types.ObjectIdentifier{Key: aws.String(k)})
		}
		if _, err := s3s.client.DeleteObjects(context.TODO(), &awss3.DeleteObjectsInput{
			Bucket: aws.String(s3s.bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		}); err != nil {
			return 0, fmt.Errorf("delete loose objects: %w", err)
		}
	}
	return len(hashes), nil
}

// looseObjects lists the loose objects: objects/xx/yyyy... (not pack/).
func (s *S3Storer) looseObjects() ([]plumbing.Hash, []string, error) {
	prefix := s.getObjectKey("objects") + "/"
	var hashes []plumbing.Hash
	var keys []string
	pager := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(context.TODO())
		if err != nil {
			return nil, nil, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			parts := strings.Split(strings.TrimPrefix(key, prefix), "/")
			if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 38 {
				continue // pack/, .gitkeep, ...
			}
			hashes = append(hashes, plumbing.NewHash(parts[0]+parts[1]))
			keys = append(keys, key)
		}
	}
	return hashes, keys, nil
}
