package gitbrowse

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"path"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Archive formats.
const (
	Zip   = "zip"
	TarGz = "tar.gz"
)

var ErrFormat = errors.New("archive format must be zip or tar.gz")

// WriteArchive writes c's tree to w as a zip or tar.gz, every file under
// prefix/ (like git archive --prefix). Submodules are skipped; symlinks are
// kept as symlinks.
func WriteArchive(w io.Writer, c *object.Commit, format, prefix string) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	when := c.Committer.When
	switch format {
	case Zip:
		zw := zip.NewWriter(w)
		err := walk(tree, func(name string, mode filemode.FileMode, f *object.File) error {
			h := &zip.FileHeader{Name: path.Join(prefix, name), Method: zip.Deflate, Modified: when}
			osMode, _ := mode.ToOSFileMode()
			h.SetMode(osMode)
			fw, err := zw.CreateHeader(h)
			if err != nil {
				return err
			}
			return copyBlob(fw, f)
		})
		if err != nil {
			return err
		}
		return zw.Close()
	case TarGz:
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		err := walk(tree, func(name string, mode filemode.FileMode, f *object.File) error {
			h := &tar.Header{Name: path.Join(prefix, name), ModTime: when, Mode: 0o644, Size: f.Size, Typeflag: tar.TypeReg}
			switch mode {
			case filemode.Executable:
				h.Mode = 0o755
			case filemode.Symlink:
				target, err := f.Contents()
				if err != nil {
					return err
				}
				h.Typeflag, h.Linkname, h.Size, h.Mode = tar.TypeSymlink, target, 0, 0o777
				return tw.WriteHeader(h)
			}
			if err := tw.WriteHeader(h); err != nil {
				return err
			}
			return copyBlob(tw, f)
		})
		if err != nil {
			return err
		}
		if err := tw.Close(); err != nil {
			return err
		}
		return gz.Close()
	default:
		return ErrFormat
	}
}

func walk(tree *object.Tree, fn func(name string, mode filemode.FileMode, f *object.File) error) error {
	files := tree.Files()
	defer files.Close()
	return files.ForEach(func(f *object.File) error {
		if f.Mode == filemode.Submodule {
			return nil
		}
		return fn(f.Name, f.Mode, f)
	})
}

func copyBlob(w io.Writer, f *object.File) error {
	r, err := f.Reader()
	if err != nil {
		return err
	}
	defer r.Close()
	_, err = io.Copy(w, r)
	return err
}
