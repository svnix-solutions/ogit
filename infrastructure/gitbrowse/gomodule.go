package gitbrowse

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

// GoMod returns the root go.mod of a commit, or nil when there is none.
func GoMod(c *object.Commit) ([]byte, error) {
	f, err := OpenFile(c, "go.mod")
	if errors.Is(err, ErrPathNotFound) || errors.Is(err, ErrNotAFile) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if f.Size > modzip.MaxGoMod || f.Mode == filemode.Symlink {
		return nil, nil
	}
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// ModulePath is the module path a go.mod declares ("" if it can't be read).
func ModulePath(gomod []byte) string { return modfile.ModulePath(gomod) }

// Requirement is a module a go.mod requires.
type Requirement struct {
	Path     string `json:"path"`
	Version  string `json:"version"`
	Indirect bool   `json:"indirect"`
}

// Requirements lists a go.mod's require directives (nil if it can't be
// parsed).
func Requirements(gomod []byte) []Requirement {
	f, err := modfile.ParseLax("go.mod", gomod, nil)
	if err != nil {
		return nil
	}
	out := make([]Requirement, 0, len(f.Require))
	for _, r := range f.Require {
		out = append(out, Requirement{Path: r.Mod.Path, Version: r.Mod.Version, Indirect: r.Indirect})
	}
	return out
}

// WriteModuleZip writes the module zip of a commit, as the go command
// expects it: x/mod's rules (no nested modules, vendored packages,
// symlinks or invalid names; size limits). Submodules are skipped; Git LFS
// pointers stay pointers.
func WriteModuleZip(w io.Writer, c *object.Commit, m module.Version) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	var files []modzip.File
	iter := tree.Files()
	defer iter.Close()
	err = iter.ForEach(func(f *object.File) error {
		if f.Mode != filemode.Submodule {
			files = append(files, zipFile{f: f, when: c.Committer.When})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return modzip.Create(w, m, files)
}

type zipFile struct {
	f    *object.File
	when time.Time
}

func (z zipFile) Path() string                 { return z.f.Name }
func (z zipFile) Lstat() (os.FileInfo, error)  { return zipInfo(z), nil }
func (z zipFile) Open() (io.ReadCloser, error) { return z.f.Reader() }

type zipInfo zipFile

func (z zipInfo) Name() string { return z.f.Name }
func (z zipInfo) Size() int64  { return z.f.Size }
func (z zipInfo) Mode() fs.FileMode {
	switch z.f.Mode {
	case filemode.Symlink:
		return fs.ModeSymlink | 0o777
	case filemode.Executable:
		return 0o755
	}
	return 0o644
}
func (z zipInfo) ModTime() time.Time { return z.when }
func (z zipInfo) IsDir() bool        { return false }
func (z zipInfo) Sys() any           { return nil }
