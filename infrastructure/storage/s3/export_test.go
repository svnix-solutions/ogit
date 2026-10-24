package s3

import "github.com/go-git/go-git/v5/plumbing"

// Test hooks for the conditional writes, to race them deterministically.

func (s *S3Storer) ReadRefForTest(name plumbing.ReferenceName) (plumbing.Hash, string, error) {
	return s.readRef(name)
}

func (s *S3Storer) PutRefIfMatchForTest(ref *plumbing.Reference, etag string) error {
	return s.putRefIfMatch(ref, etag)
}
