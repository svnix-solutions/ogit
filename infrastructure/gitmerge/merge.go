// Package gitmerge merges branches for pull requests, without a git binary
// and on any storer (local or S3).
//
// A path changed on one side since the merge base takes that side. One
// changed on both is merged line by line (lines.go): it conflicts when the
// same lines changed, and always when it's binary, over MaxLineMergeSize or
// deleted on one side. Renames are not followed. New objects are built in memory and written to the repository as one pack only when
// the whole merge succeeded, so a failed merge leaves nothing behind.
package gitmerge

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Methods.
const (
	MethodMerge  = "merge"  // a merge commit with both heads as parents
	MethodSquash = "squash" // one commit with the combined changes
	MethodRebase = "rebase" // head's commits replayed on top of base
)

var (
	ErrMethod         = errors.New("merge method must be merge, squash or rebase")
	ErrNothingToMerge = errors.New("the branch has no changes to merge")
	ErrRebaseMerges   = errors.New("rebase can't replay merge commits; use merge or squash")
)

// ConflictError lists the paths that can't be merged, sorted.
type ConflictError struct {
	Paths []string
	Files []ConflictFile
}

func newConflictError(files map[string]*ConflictFile) *ConflictError {
	e := &ConflictError{}
	for path := range files {
		e.Paths = append(e.Paths, path)
	}
	sort.Strings(e.Paths)
	for _, path := range e.Paths {
		e.Files = append(e.Files, *files[path])
	}
	return e
}

func (e *ConflictError) Error() string {
	return "conflicts in " + strings.Join(e.Paths, ", ")
}

// Request describes a merge of Head into Base.
type Request struct {
	Base, Head *object.Commit
	Method     string
	Message    string           // merge and squash commits
	Committer  object.Signature // the person merging; also the author of merge and squash commits
}

// Result is the new commit for the base branch and the objects to write.
type Result struct {
	Commit  plumbing.Hash
	objects *memory.Storage
}

// Merge computes the merge. Nothing is written to the repository.
func Merge(st storer.EncodedObjectStorer, req Request) (*Result, error) {
	bases, err := req.Head.MergeBase(req.Base)
	if err != nil {
		return nil, err
	}
	if len(bases) > 0 && bases[0].Hash == req.Head.Hash {
		return nil, ErrNothingToMerge // head is already in base
	}
	var mergeBase *object.Commit
	if len(bases) > 0 {
		mergeBase = bases[0]
	}

	out := memory.NewStorage()
	w := &writer{EncodedObjectStorer: st, out: out}
	switch req.Method {
	case MethodMerge, MethodSquash:
		tree, err := w.mergeTrees(mergeBase, req.Base, req.Head)
		if err != nil {
			return nil, err
		}
		parents := []plumbing.Hash{req.Base.Hash}
		if req.Method == MethodMerge {
			parents = append(parents, req.Head.Hash)
		}
		h, err := w.commit(&object.Commit{Author: req.Committer, Committer: req.Committer, Message: req.Message, TreeHash: tree, ParentHashes: parents})
		if err != nil {
			return nil, err
		}
		return &Result{Commit: h, objects: out}, nil
	case MethodRebase:
		commits, err := toReplay(req.Head, mergeBase)
		if err != nil {
			return nil, err
		}
		current := req.Base
		head := req.Base.Hash
		for _, c := range commits {
			parent, err := c.Parent(0)
			if err != nil {
				return nil, err
			}
			tree, err := w.mergeTrees(parent, current, c)
			if err != nil {
				return nil, err
			}
			head, err = w.commit(&object.Commit{Author: c.Author, Committer: req.Committer, Message: c.Message, TreeHash: tree, ParentHashes: []plumbing.Hash{head}})
			if err != nil {
				return nil, err
			}
			// The next commit merges against what was just made.
			obj, err := out.EncodedObject(plumbing.CommitObject, head)
			if err != nil {
				return nil, err
			}
			if current, err = object.DecodeCommit(w, obj); err != nil {
				return nil, err
			}
		}
		return &Result{Commit: head, objects: out}, nil
	default:
		return nil, ErrMethod
	}
}

// Check reports the paths merging head into base would conflict in (none
// means it can be merged with any method but rebase, which may still conflict
// commit by commit).
func Check(st storer.EncodedObjectStorer, base, head *object.Commit) (conflicts []string, upToDate bool, err error) {
	files, upToDate, err := Conflicts(st, base, head)
	for _, f := range files {
		conflicts = append(conflicts, f.Path)
	}
	return conflicts, upToDate, err
}

// Conflicts is Check with the details of each path.
func Conflicts(st storer.EncodedObjectStorer, base, head *object.Commit) (files []ConflictFile, upToDate bool, err error) {
	bases, err := head.MergeBase(base)
	if err != nil {
		return nil, false, err
	}
	if len(bases) > 0 && bases[0].Hash == head.Hash {
		return nil, true, nil
	}
	var mergeBase *object.Commit
	if len(bases) > 0 {
		mergeBase = bases[0]
	}
	w := &writer{EncodedObjectStorer: st, out: memory.NewStorage()}
	_, err = w.mergeTrees(mergeBase, base, head)
	var conflict *ConflictError
	if errors.As(err, &conflict) {
		return conflict.Files, false, nil
	}
	return nil, false, err
}

// toReplay returns head's commits since base, oldest first, following first
// parents. Merge commits can't be replayed.
func toReplay(head, base *object.Commit) ([]*object.Commit, error) {
	var commits []*object.Commit
	for c := head; base == nil || c.Hash != base.Hash; {
		if c.NumParents() > 1 {
			return nil, ErrRebaseMerges
		}
		commits = append(commits, c)
		if c.NumParents() == 0 {
			if base != nil {
				return nil, fmt.Errorf("merge base not found on the first-parent history")
			}
			break
		}
		parent, err := c.Parent(0)
		if err != nil {
			return nil, err
		}
		c = parent
	}
	for i, j := 0, len(commits)-1; i < j; i, j = i+1, j-1 {
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// Write stores the result's objects in st: as one pack when st supports it.
func Write(st storer.Storer, res *Result) error {
	var hashes []plumbing.Hash
	iter, err := res.objects.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return err
	}
	if err := iter.ForEach(func(o plumbing.EncodedObject) error {
		hashes = append(hashes, o.Hash())
		return nil
	}); err != nil {
		return err
	}
	if pw, ok := st.(storer.PackfileWriter); ok {
		w, err := pw.PackfileWriter()
		if err != nil {
			return err
		}
		if _, err := packfile.NewEncoder(w, res.objects, false).Encode(hashes, 10); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	for _, h := range hashes {
		obj, err := res.objects.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			return err
		}
		if _, err := st.SetEncodedObject(obj); err != nil {
			return err
		}
	}
	return nil
}

// writer reads existing objects from the repository and new ones from out,
// where it also writes.
type writer struct {
	storer.EncodedObjectStorer // the repository, read-only here
	out                        *memory.Storage
}

func (w *writer) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	if obj, err := w.out.EncodedObject(t, h); err == nil {
		return obj, nil
	}
	return w.EncodedObjectStorer.EncodedObject(t, h)
}

func (w *writer) commit(c *object.Commit) (plumbing.Hash, error) {
	obj := w.out.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return w.out.SetEncodedObject(obj)
}

type entry struct {
	mode filemode.FileMode
	hash plumbing.Hash
}

// mergeTrees three-way merges the trees of ours and theirs against base (nil
// for unrelated histories) and returns the resulting tree.
func (w *writer) mergeTrees(base, ours, theirs *object.Commit) (plumbing.Hash, error) {
	b, err := w.flatten(base)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	o, err := w.flatten(ours)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	t, err := w.flatten(theirs)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	result := map[string]entry{}
	conflicts := map[string]*ConflictFile{}
	paths := map[string]bool{}
	for _, m := range []map[string]entry{b, o, t} {
		for p := range m {
			paths[p] = true
		}
	}
	for p := range paths {
		be, inB := b[p]
		oe, inO := o[p]
		te, inT := t[p]
		switch {
		case inO == inT && oe == te: // same on both sides
		case inB == inO && be == oe: // only theirs changed it
			oe, inO = te, inT
		case inB == inT && be == te: // only ours changed it
		default:
			merged, conflict, err := w.mergeFile(p, be, inB, oe, inO, te, inT)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			if conflict != nil {
				conflicts[p] = conflict
				continue
			}
			oe = *merged
		}
		if inO {
			result[p] = oe
		}
	}
	// A file on one side where the other has a directory.
	for p := range result {
		for dir := parentDir(p); dir != ""; dir = parentDir(dir) {
			if _, ok := result[dir]; ok {
				conflicts[dir] = &ConflictFile{Path: dir, Reason: ConflictType}
			}
		}
	}
	if len(conflicts) > 0 {
		return plumbing.ZeroHash, newConflictError(conflicts)
	}
	return w.buildTree(result)
}

func (w *writer) flatten(c *object.Commit) (map[string]entry, error) {
	files := map[string]entry{}
	if c == nil {
		return files, nil
	}
	tree, err := object.GetTree(w, c.TreeHash)
	if err != nil {
		return nil, err
	}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, e, err := walker.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		} else if err != nil {
			return nil, err
		}
		if e.Mode != filemode.Dir {
			files[name] = entry{e.Mode, e.Hash}
		}
	}
}

// buildTree writes the tree objects for a flat path → entry map.
func (w *writer) buildTree(files map[string]entry) (plumbing.Hash, error) {
	type dir struct {
		files map[string]entry
		dirs  map[string]*dir
	}
	newDir := func() *dir { return &dir{files: map[string]entry{}, dirs: map[string]*dir{}} }
	root := newDir()
	for p, e := range files {
		d := root
		parts := strings.Split(p, "/")
		for _, part := range parts[:len(parts)-1] {
			if d.dirs[part] == nil {
				d.dirs[part] = newDir()
			}
			d = d.dirs[part]
		}
		d.files[parts[len(parts)-1]] = e
	}
	var write func(d *dir) (plumbing.Hash, error)
	write = func(d *dir) (plumbing.Hash, error) {
		var entries []object.TreeEntry
		for name, e := range d.files {
			entries = append(entries, object.TreeEntry{Name: name, Mode: e.mode, Hash: e.hash})
		}
		for name, sub := range d.dirs {
			h, err := write(sub)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h})
		}
		// git's order: directories sort as if their name ended with "/".
		key := func(e object.TreeEntry) string {
			if e.Mode == filemode.Dir {
				return e.Name + "/"
			}
			return e.Name
		}
		sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
		obj := w.out.NewEncodedObject()
		if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
			return plumbing.ZeroHash, err
		}
		return w.out.SetEncodedObject(obj)
	}
	return write(root)
}

func parentDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i]
	}
	return ""
}
