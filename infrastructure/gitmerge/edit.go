package gitmerge

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage/memory"
)

// Edit computes a commit on parent (nil: the first one) that sets files
// (path → content; nil removes the path) and keeps the others, for edits
// made without a working tree (wiki pages). Nothing is written: Write
// stores the result.
func Edit(st storer.EncodedObjectStorer, parent *object.Commit, files map[string][]byte, author object.Signature, message string) (*Result, error) {
	w := &writer{EncodedObjectStorer: st, out: memory.NewStorage()}
	tree, err := w.flatten(parent)
	if err != nil {
		return nil, err
	}
	for path, content := range files {
		if content == nil {
			delete(tree, path)
			continue
		}
		obj := w.out.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		wr, err := obj.Writer()
		if err != nil {
			return nil, err
		}
		if _, err := wr.Write(content); err != nil {
			return nil, err
		}
		if err := wr.Close(); err != nil {
			return nil, err
		}
		h, err := w.out.SetEncodedObject(obj)
		if err != nil {
			return nil, err
		}
		mode := filemode.Regular
		if old, ok := tree[path]; ok && old.mode == filemode.Executable {
			mode = old.mode
		}
		tree[path] = entry{mode, h}
	}
	root, err := w.buildTree(tree)
	if err != nil {
		return nil, err
	}
	c := &object.Commit{Author: author, Committer: author, Message: message, TreeHash: root}
	if parent != nil {
		c.ParentHashes = []plumbing.Hash{parent.Hash}
	}
	h, err := w.commit(c)
	if err != nil {
		return nil, err
	}
	return &Result{Commit: h, objects: w.out}, nil
}
