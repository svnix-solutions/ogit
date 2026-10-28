package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Files of an area (release assets, Actions artifacts and cache) live in
// <repository>.git/<area>/<id>, so they go with the repository. git
// ignores these directories.

func (ls *LocalStorage) areaPath(repoPath, area, id string) (string, error) {
	if !storageAreas[area] || id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", errors.New("invalid file id")
	}
	return filepath.Join(ls.getFullPath(repoPath), area, id), nil
}

var storageAreas = map[string]bool{"release-assets": true, "artifacts": true, "actions-cache": true}

func (ls *LocalStorage) assetPath(repoPath, id string) (string, error) {
	return ls.areaPath(repoPath, "release-assets", id)
}

func (ls *LocalStorage) PutAsset(repoPath, id string, r io.Reader) error {
	return ls.PutFile(repoPath, "release-assets", id, r)
}

// PutAsset writes r to a temporary file and renames it, so readers never
// see a partial asset.
func (ls *LocalStorage) PutFile(repoPath, area, id string, r io.Reader) error {
	if !ls.RepositoryExists(repoPath) {
		return errors.New("repository does not exist")
	}
	p, err := ls.areaPath(repoPath, area, id)
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
	return ls.OpenFile(repoPath, "release-assets", id)
}

func (ls *LocalStorage) OpenFile(repoPath, area, id string) (io.ReadCloser, error) {
	p, err := ls.areaPath(repoPath, area, id)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (ls *LocalStorage) DeleteAsset(repoPath, id string) error {
	return ls.DeleteFile(repoPath, "release-assets", id)
}

func (ls *LocalStorage) DeleteFile(repoPath, area, id string) error {
	p, err := ls.areaPath(repoPath, area, id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (ls *LocalStorage) OpenFileRange(repoPath, area, id string, offset, length int64) (io.ReadCloser, error) {
	p, err := ls.areaPath(repoPath, area, id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(f, length), f}, nil
}
