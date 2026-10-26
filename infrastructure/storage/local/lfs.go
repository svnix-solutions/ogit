package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// LFS objects live in <repository>.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>,
// git-lfs' own layout, so they go with the repository.

func (ls *LocalStorage) lfsPath(repoPath, oid string) string {
	return filepath.Join(ls.getFullPath(repoPath), "lfs", "objects", oid[0:2], oid[2:4], oid)
}

// PutLFSObject writes r to a temporary file and renames it into place.
func (ls *LocalStorage) PutLFSObject(repoPath, oid string, r io.Reader) error {
	if !ls.RepositoryExists(repoPath) {
		return errors.New("repository does not exist")
	}
	p := ls.lfsPath(repoPath, oid)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

func (ls *LocalStorage) OpenLFSObject(repoPath, oid string) (io.ReadCloser, error) {
	return os.Open(ls.lfsPath(repoPath, oid))
}

// StatLFSObject returns the object's size, or ok false when it is missing.
func (ls *LocalStorage) StatLFSObject(repoPath, oid string) (int64, bool, error) {
	info, err := os.Stat(ls.lfsPath(repoPath, oid))
	if os.IsNotExist(err) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	return info.Size(), true, nil
}
