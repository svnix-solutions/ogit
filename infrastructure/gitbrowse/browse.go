// Package gitbrowse reads repositories for the web UI: refs, trees, files,
// history, diffs, blame and archives. It works on a go-git storer.Storer
// (local or S3) without git.Repository, which S3Storer does not implement.
//
// Everything here is bounded (file sizes, diff sizes, commit counts) so
// that one request can't read a whole large repository.
package gitbrowse

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

var (
	ErrRefNotFound  = errors.New("branch, tag or commit not found")
	ErrPathNotFound = errors.New("path not found")
	ErrNotAFile     = errors.New("not a file")
	ErrEmpty        = errors.New("repository is empty")
)

// Signature is a commit's author or committer.
type Signature struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	When  time.Time `json:"when"`
}

// Commit is a commit's metadata.
type Commit struct {
	Hash      string    `json:"hash"`
	Parents   []string  `json:"parents"`
	Message   string    `json:"message"`
	Author    Signature `json:"author"`
	Committer Signature `json:"committer"`
}

// CommitInfo returns c's metadata.
func CommitInfo(c *object.Commit) Commit { return toCommit(c) }

func toCommit(c *object.Commit) Commit {
	parents := make([]string, 0, len(c.ParentHashes))
	for _, p := range c.ParentHashes {
		parents = append(parents, p.String())
	}
	return Commit{
		Hash:      c.Hash.String(),
		Parents:   parents,
		Message:   c.Message,
		Author:    Signature{c.Author.Name, c.Author.Email, c.Author.When},
		Committer: Signature{c.Committer.Name, c.Committer.Email, c.Committer.When},
	}
}

// Ref is a branch or tag and the commit it points to.
type Ref struct {
	Name   string `json:"name"`
	Commit Commit `json:"commit"`
}

// Refs lists branches and tags (newest commit first), and the default
// branch (HEAD's target).
func Refs(st storer.Storer) (defaultBranch string, branches, tags []Ref, err error) {
	if head, err := st.Reference(plumbing.HEAD); err == nil && head.Type() == plumbing.SymbolicReference {
		defaultBranch = head.Target().Short()
	}
	iter, err := st.IterReferences()
	if err != nil {
		return "", nil, nil, err
	}
	err = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() != plumbing.HashReference {
			return nil
		}
		c, err := peel(st, r.Hash())
		if err != nil {
			return nil // a ref to a missing or non-commit object: skip it
		}
		switch {
		case r.Name().IsBranch():
			branches = append(branches, Ref{r.Name().Short(), toCommit(c)})
		case r.Name().IsTag():
			tags = append(tags, Ref{r.Name().Short(), toCommit(c)})
		}
		return nil
	})
	byDate := func(refs []Ref) {
		sort.Slice(refs, func(i, j int) bool { return refs[i].Commit.Committer.When.After(refs[j].Commit.Committer.When) })
	}
	byDate(branches)
	byDate(tags)
	return defaultBranch, branches, tags, err
}

// peel returns the commit h points to, following annotated tags.
func peel(st storer.EncodedObjectStorer, h plumbing.Hash) (*object.Commit, error) {
	for range 10 {
		obj, err := st.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			return nil, err
		}
		switch obj.Type() {
		case plumbing.CommitObject:
			return object.DecodeCommit(st, obj)
		case plumbing.TagObject:
			tag, err := object.DecodeTag(st, obj)
			if err != nil {
				return nil, err
			}
			h = tag.Target
		default:
			return nil, ErrRefNotFound
		}
	}
	return nil, ErrRefNotFound
}

// ResolveRef returns the commit for a branch, tag, full commit hash or HEAD
// ("" means HEAD).
func ResolveRef(st storer.Storer, ref string) (*object.Commit, error) {
	if ref == "" || ref == "HEAD" {
		head, err := storer.ResolveReference(st, plumbing.HEAD)
		if err != nil {
			return nil, ErrEmpty
		}
		return peel(st, head.Hash())
	}
	for _, name := range []plumbing.ReferenceName{plumbing.NewBranchReferenceName(ref), plumbing.NewTagReferenceName(ref)} {
		if r, err := storer.ResolveReference(st, name); err == nil {
			return peel(st, r.Hash())
		}
	}
	if len(ref) == 40 && plumbing.IsHash(ref) {
		if c, err := peel(st, plumbing.NewHash(ref)); err == nil {
			return c, nil
		}
	}
	return nil, ErrRefNotFound
}

// SplitRefPath splits "<ref>/<path>" from a URL, where the ref itself may
// contain slashes (feature/x): the longest branch or tag name that prefixes
// spec wins, as on GitHub; otherwise the first segment is the ref (a commit
// hash or HEAD).
func SplitRefPath(st storer.Storer, spec string) (ref, path string, c *object.Commit, err error) {
	spec = strings.Trim(spec, "/")
	best := ""
	iter, err := st.IterReferences()
	if err != nil {
		return "", "", nil, err
	}
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if !r.Name().IsBranch() && !r.Name().IsTag() {
			return nil
		}
		name := r.Name().Short()
		if (spec == name || strings.HasPrefix(spec, name+"/")) && len(name) > len(best) {
			best = name
		}
		return nil
	})
	if best == "" {
		best, _, _ = strings.Cut(spec, "/")
	}
	c, err = ResolveRef(st, best)
	if err != nil {
		return "", "", nil, err
	}
	return best, strings.Trim(strings.TrimPrefix(spec, best), "/"), c, nil
}

// Entry is one item of a directory.
type Entry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"` // dir, file, symlink or submodule
	Size int64  `json:"size,omitempty"`
}

// Tree lists the directory at path ("" for the root): directories first,
// then by name.
func Tree(c *object.Commit, path string) ([]Entry, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	if path != "" {
		if tree, err = tree.Tree(path); err != nil {
			if errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
				return nil, ErrPathNotFound
			}
			return nil, err
		}
	}
	entries := make([]Entry, 0, len(tree.Entries))
	for _, e := range tree.Entries {
		entry := Entry{Name: e.Name, Path: strings.TrimPrefix(path+"/"+e.Name, "/")}
		switch e.Mode {
		case filemode.Dir:
			entry.Type = "dir"
		case filemode.Submodule:
			entry.Type = "submodule"
		case filemode.Symlink:
			entry.Type = "symlink"
		default:
			entry.Type = "file"
			if size, err := tree.Size(e.Name); err == nil {
				entry.Size = size
			}
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if (entries[i].Type == "dir") != (entries[j].Type == "dir") {
			return entries[i].Type == "dir"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// readmeNames in order of preference.
var readmeNames = []string{"readme.md", "readme.markdown", "readme", "readme.txt", "readme.rst"}

// Readme returns the README among entries, if any.
func Readme(entries []Entry) *Entry {
	for _, want := range readmeNames {
		for i := range entries {
			if entries[i].Type == "file" && strings.ToLower(entries[i].Name) == want {
				return &entries[i]
			}
		}
	}
	return nil
}
