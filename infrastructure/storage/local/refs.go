package local

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// atomicRefs fixes reference writes of go-git's filesystem storer, which
// rewrites a ref file in place (truncate, then write): a request reading
// the branch at that moment sees an empty file and reports it missing.
// Here a ref is written to a temporary file and renamed over the old one,
// which readers see atomically, and compare-and-swap is serialized per
// repository (one server owns local storage).
type atomicRefs struct {
	*filesystem.Storage
	dir string
	mu  *sync.Mutex
}

var repoLocks sync.Map // repository directory -> *sync.Mutex

func newAtomicRefs(st *filesystem.Storage, dir string) *atomicRefs {
	mu, _ := repoLocks.LoadOrStore(dir, &sync.Mutex{})
	return &atomicRefs{Storage: st, dir: dir, mu: mu.(*sync.Mutex)}
}

// go-git's filesystem storer lists the packs once, on the first object it
// reads, and keeps that list. A push or merge served by another request
// writes a new pack and then moves a branch to a commit in it, so a request
// that reads the branch afterwards would not find the commit (reported as
// "branch not found"). On a miss, list the packs again and retry once.

func (s *atomicRefs) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	obj, err := s.Storage.EncodedObject(t, h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		s.Storage.Reindex()
		obj, err = s.Storage.EncodedObject(t, h)
	}
	return obj, err
}

func (s *atomicRefs) HasEncodedObject(h plumbing.Hash) error {
	err := s.Storage.HasEncodedObject(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		s.Storage.Reindex()
		err = s.Storage.HasEncodedObject(h)
	}
	return err
}

func (s *atomicRefs) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	size, err := s.Storage.EncodedObjectSize(h)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		s.Storage.Reindex()
		size, err = s.Storage.EncodedObjectSize(h)
	}
	return size, err
}

func (s *atomicRefs) SetReference(ref *plumbing.Reference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(ref)
}

func (s *atomicRefs) CheckAndSetReference(ref, old *plumbing.Reference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old != nil {
		current, err := s.Storage.Reference(old.Name())
		if err != nil {
			return err
		}
		if current.Hash() != old.Hash() {
			return storage.ErrReferenceHasChanged
		}
	}
	return s.write(ref)
}

func (s *atomicRefs) RemoveReference(name plumbing.ReferenceName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Storage.RemoveReference(name)
}

func (s *atomicRefs) write(ref *plumbing.Reference) error {
	content := ref.Hash().String() + "\n"
	if ref.Type() == plumbing.SymbolicReference {
		content = "ref: " + ref.Target().String() + "\n"
	}
	path := filepath.Join(s.dir, filepath.FromSlash(ref.Name().String()))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Outside refs/, so a crash never leaves a file that looks like a ref;
	// same filesystem, so the rename stays atomic.
	tmpDir := filepath.Join(s.dir, "ogit-tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "ref-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
