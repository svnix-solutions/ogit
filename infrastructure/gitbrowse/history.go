package gitbrowse

import (
	"errors"
	"io"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// maxLogScan bounds how many commits one Log call reads when filtering by
// path, so a path touched once long ago doesn't read the whole history.
const maxLogScan = 5000

// Log returns commits reachable from c, newest first, that change path
// ("" for all), skipping offset and returning at most limit. more says if
// there are further ones.
func Log(c *object.Commit, path string, offset, limit int) (commits []Commit, more bool, err error) {
	iter := object.NewCommitIterCTime(c, nil, nil)
	defer iter.Close()
	scanned, matched := 0, 0
	for {
		commit, err := iter.Next()
		if errors.Is(err, io.EOF) {
			return commits, false, nil
		} else if err != nil {
			return nil, false, err
		}
		if scanned++; scanned > maxLogScan {
			return commits, true, nil
		}
		if path != "" {
			changed, err := changesPath(commit, path)
			if err != nil {
				return nil, false, err
			}
			if !changed {
				continue
			}
		}
		if matched++; matched <= offset {
			continue
		}
		if len(commits) == limit {
			return commits, true, nil
		}
		commits = append(commits, toCommit(commit))
	}
}

// changesPath reports whether commit changed path compared with its first
// parent (or added it, for a root commit).
func changesPath(commit *object.Commit, path string) (bool, error) {
	here, err := entryHash(commit, path)
	if err != nil {
		return false, err
	}
	if commit.NumParents() == 0 {
		return !here.IsZero(), nil
	}
	parent, err := commit.Parent(0)
	if err != nil {
		return false, err
	}
	before, err := entryHash(parent, path)
	if err != nil {
		return false, err
	}
	return here != before, nil
}

// entryHash is the hash of the blob or tree at path, zero if absent.
func entryHash(c *object.Commit, path string) (plumbing.Hash, error) {
	tree, err := c.Tree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	e, err := tree.FindEntry(path)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return plumbing.ZeroHash, nil
	} else if err != nil {
		return plumbing.ZeroHash, err
	}
	return e.Hash, nil
}

// GetCommit returns the commit with this full hash.
func GetCommit(st storer.EncodedObjectStorer, hash string) (*object.Commit, error) {
	if len(hash) != 40 || !plumbing.IsHash(hash) {
		return nil, ErrRefNotFound
	}
	c, err := object.GetCommit(st, plumbing.NewHash(hash))
	if err != nil {
		return nil, ErrRefNotFound
	}
	return c, nil
}

// Comparison is what head adds on top of base.
type Comparison struct {
	MergeBase string   `json:"merge_base"`
	Commits   []Commit `json:"commits"` // oldest first
	// CommitsTruncated: there were more than maxCompareCommits.
	CommitsTruncated bool `json:"commits_truncated"`
	Diff             Diff `json:"diff"`
}

const maxCompareCommits = 250

// Compare returns the commits in head that are not in base, and the diff
// from their merge base to head (what a pull request of head into base
// would change).
func Compare(base, head *object.Commit, limits Limits) (*Comparison, error) {
	bases, err := head.MergeBase(base)
	if err != nil {
		return nil, err
	}
	out := &Comparison{Commits: []Commit{}}
	var from *object.Commit
	var stop []plumbing.Hash
	if len(bases) > 0 {
		from = bases[0]
		out.MergeBase = from.Hash.String()
		for _, b := range bases {
			stop = append(stop, b.Hash)
		}
	}

	// Commits reachable from head but not through a merge base.
	iter := object.NewCommitPreorderIter(head, nil, stop)
	err = iter.ForEach(func(c *object.Commit) error {
		if len(out.Commits) == maxCompareCommits {
			out.CommitsTruncated = true
			return storer.ErrStop
		}
		out.Commits = append(out.Commits, toCommit(c))
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(out.Commits)-1; i < j; i, j = i+1, j-1 {
		out.Commits[i], out.Commits[j] = out.Commits[j], out.Commits[i]
	}

	diff, err := DiffCommits(from, head, limits)
	if err != nil {
		return nil, err
	}
	out.Diff = *diff
	return out, nil
}

// BlameLine is one line with the commit that last changed it.
type BlameLine struct {
	Text   string `json:"text"`
	Hash   string `json:"hash"`
	Author string `json:"author"`
	When   string `json:"when"` // RFC 3339
}

// Blame returns who last changed each line of path. It refuses binary
// files and files larger than maxBytes.
func Blame(c *object.Commit, path string, maxBytes int64) ([]BlameLine, error) {
	f, err := ReadFile(c, path, maxBytes)
	if err != nil {
		return nil, err
	}
	if f.Binary || f.Truncated {
		return nil, ErrTooLarge
	}
	res, err := git.Blame(c, path)
	if err != nil {
		return nil, err
	}
	lines := make([]BlameLine, 0, len(res.Lines))
	for _, l := range res.Lines {
		lines = append(lines, BlameLine{Text: l.Text, Hash: l.Hash.String(), Author: l.AuthorName, When: l.Date.UTC().Format("2006-01-02T15:04:05Z07:00")})
	}
	return lines, nil
}

// ErrTooLarge: the file is binary or above the limit for this view.
var ErrTooLarge = errors.New("file is binary or too large for this view")
