package local

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Package files live in <path>/packages/<owner id>/<name>, beside the
// repositories (which end in .git).

func (ls *LocalStorage) packagePath(ownerId, name string) (string, error) {
	if ownerId == "" || strings.ContainsAny(ownerId, "/\\") || ownerId == "." || ownerId == ".." {
		return "", errors.New("invalid package owner")
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", errors.New("invalid package path")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid package path")
		}
	}
	return filepath.Join(ls.basePath, "packages", ownerId, filepath.FromSlash(name)), nil
}

// PutPackageFile writes r to a temporary file and renames it.
func (ls *LocalStorage) PutPackageFile(ownerId, name string, r io.Reader) error {
	p, err := ls.packagePath(ownerId, name)
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

func (ls *LocalStorage) OpenPackageFile(ownerId, name string) (io.ReadCloser, error) {
	p, err := ls.packagePath(ownerId, name)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (ls *LocalStorage) DeletePackageFile(ownerId, name string) error {
	p, err := ls.packagePath(ownerId, name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
