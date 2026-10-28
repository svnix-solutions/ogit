package git

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Forks and pull requests from forks copy objects between repositories,
// whatever their storage: the objects reachable from some tips in the
// source, minus those reachable from what the destination already has, are
// written to the destination as one pack.

// CopyObjects copies to dst the objects reachable from tips in src that are
// not reachable from haves (commits dst has; others are ignored). It
// returns how many objects were copied.
func CopyObjects(src storer.EncodedObjectStorer, dst storer.Storer, tips, haves []plumbing.Hash) (int, error) {
	var known []plumbing.Hash
	for _, h := range haves {
		if !h.IsZero() && dst.HasEncodedObject(h) == nil {
			known = append(known, h)
		}
	}
	hashes, err := revlist.ObjectsWithStorageForIgnores(src, dst, tips, known)
	if err != nil {
		return 0, fmt.Errorf("listing objects to copy: %w", err)
	}
	if len(hashes) == 0 {
		return 0, nil
	}
	pw, ok := dst.(storer.PackfileWriter)
	if !ok {
		return 0, errors.New("destination storage cannot write packs")
	}
	w, err := pw.PackfileWriter()
	if err != nil {
		return 0, err
	}
	if _, err := packfile.NewEncoder(w, src, false).Encode(hashes, 10); err != nil {
		_ = w.Close()
		return 0, fmt.Errorf("writing pack: %w", err)
	}
	return len(hashes), w.Close()
}

// Overlay reads objects from Primary, then from Secondary: comparing a
// fork's branch with the original without copying anything. It is read
// only.
type Overlay struct {
	Primary, Secondary storer.EncodedObjectStorer
}

func (o Overlay) NewEncodedObject() plumbing.EncodedObject { return o.Primary.NewEncodedObject() }

func (o Overlay) SetEncodedObject(plumbing.EncodedObject) (plumbing.Hash, error) {
	return plumbing.ZeroHash, errors.New("read-only overlay")
}

func (o Overlay) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	obj, err := o.Primary.EncodedObject(t, h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return o.Secondary.EncodedObject(t, h)
	}
	return obj, err
}

func (o Overlay) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	return o.Primary.IterEncodedObjects(t)
}

func (o Overlay) HasEncodedObject(h plumbing.Hash) error {
	if err := o.Primary.HasEncodedObject(h); err == nil {
		return nil
	}
	return o.Secondary.HasEncodedObject(h)
}

func (o Overlay) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	size, err := o.Primary.EncodedObjectSize(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return o.Secondary.EncodedObjectSize(h)
	}
	return size, err
}

func (o Overlay) AddAlternate(string) error { return errors.New("read-only overlay") }
