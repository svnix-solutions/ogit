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
// Git LFS pointers are replaced by their content when lfs (if not nil)
// opens it, and kept otherwise.
func WriteArchive(w io.Writer, c *object.Commit, format, prefix string, lfs LFSStore) error {
	tree, err := c.Tree()
	if err != nil {
		return err
	}
	when := c.Committer.When
	switch format {
	case Zip:
		zw := zip.NewWriter(w)
		err := walk(tree, lfs, func(name string, mode filemode.FileMode, f blob) error {
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
		err := walk(tree, lfs, func(name string, mode filemode.FileMode, f blob) error {
			h := &tar.Header{Name: path.Join(prefix, name), ModTime: when, Mode: 0o644, Size: f.size, Typeflag: tar.TypeReg}
			switch mode {
			case filemode.Executable:
				h.Mode = 0o755
			case filemode.Symlink:
				target, err := f.file.Contents()
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

// blob is a file in an archive: its git blob, or its LFS content.
type blob struct {
	file *object.File
	size int64
	lfs  *LFSPointer
	open LFSStore
}

func walk(tree *object.Tree, lfs LFSStore, fn func(name string, mode filemode.FileMode, f blob) error) error {
	files := tree.Files()
	defer files.Close()
	return files.ForEach(func(f *object.File) error {
		if f.Mode == filemode.Submodule {
			return nil
		}
		b := blob{file: f, size: f.Size}
		if lfs != nil {
			if p, ok := FilePointer(f); ok {
				// Only pointers whose content is there are replaced.
				if lfs.Present(p.Oid, p.Size) {
					b.size, b.lfs, b.open = p.Size, p, lfs
				}
			}
		}
		return fn(f.Name, f.Mode, b)
	})
}

func copyBlob(w io.Writer, f blob) error {
	var r io.ReadCloser
	var err error
	if f.lfs != nil {
		r, err = f.open.Open(f.lfs.Oid, f.lfs.Size)
	} else {
		r, err = f.file.Reader()
	}
	if err != nil {
		return err
	}
	defer r.Close()
	n, err := io.Copy(w, r)
	if err == nil && n != f.size {
		return io.ErrUnexpectedEOF
	}
	return err
}
