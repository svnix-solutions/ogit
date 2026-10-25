package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Release assets live in <repository>.git/release-assets/<id>, so they go
// with the repository. git ignores the directory.

func (ls *LocalStorage) assetPath(repoPath, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", errors.New("invalid asset id")
	}
	return filepath.Join(ls.getFullPath(repoPath), "release-assets", id), nil
}

// PutAsset writes r to a temporary file and renames it, so readers never
// see a partial asset.
func (ls *LocalStorage) PutAsset(repoPath, id string, r io.Reader) error {
	if !ls.RepositoryExists(repoPath) {
		return errors.New("repository does not exist")
	}
	p, err := ls.assetPath(repoPath, id)
	if err != nil {
		return err
	}
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

func (ls *LocalStorage) OpenAsset(repoPath, id string) (io.ReadCloser, error) {
	p, err := ls.assetPath(repoPath, id)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (ls *LocalStorage) DeleteAsset(repoPath, id string) error {
	p, err := ls.assetPath(repoPath, id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
