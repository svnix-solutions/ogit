package s3

import "github.com/go-git/go-git/v5/plumbing"

// Test hooks for the conditional writes, to race them deterministically.

func (s *S3Storer) ReadRefForTest(name plumbing.ReferenceName) (plumbing.Hash, string, error) {
	return s.readRef(name)
}

func (s *S3Storer) PutRefIfMatchForTest(ref *plumbing.Reference, etag string) error {
	return s.putRefIfMatch(ref, etag)
}

func MergeSplitForTest(counts []int64) int { return mergeSplit(counts) }

// VisiblePacksForTest returns the packs a new request would read.
func (s *S3Storer) VisiblePacksForTest() ([]string, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, p := range st.packs {
		keys = append(keys, p.key)
	}
	return keys, nil
}
