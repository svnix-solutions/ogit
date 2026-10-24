package gitbrowse

import (
	"bytes"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// File is a file's content for display.
type File struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"` // larger than the display limit
	Symlink   bool   `json:"symlink,omitempty"`
	Content   string `json:"content,omitempty"` // UTF-8 text, empty when binary
}

// OpenFile returns the blob at path, for streaming (raw downloads).
func OpenFile(c *object.Commit, path string) (*object.File, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, err
	}
	f, err := tree.File(path)
	if errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
		return nil, ErrPathNotFound
	} else if err != nil {
		return nil, err
	}
	if f.Mode == filemode.Submodule || f.Mode == filemode.Dir {
		return nil, ErrNotAFile
	}
	return f, nil
}

// ReadFile returns up to max bytes of the file at path as text, or marks it
// binary.
func ReadFile(c *object.Commit, path string, max int64) (*File, error) {
	f, err := OpenFile(c, path)
	if err != nil {
		return nil, err
	}
	out := &File{Path: path, Size: f.Size, Symlink: f.Mode == filemode.Symlink}
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, max))
	if err != nil {
		return nil, err
	}
	out.Truncated = f.Size > max
	if IsBinary(data) {
		out.Binary = true
		return out, nil
	}
	out.Content = string(data)
	return out, nil
}

// IsBinary uses git's rule (a NUL byte in the first 8000 bytes), plus
// invalid UTF-8 there. The sample may end inside a multi-byte character, so
// up to 3 trailing bytes are ignored.
func IsBinary(data []byte) bool {
	head := data[:min(len(data), 8000)]
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	for i := 0; i < 4 && len(head) > 0; i++ {
		if utf8.Valid(head) {
			return false
		}
		head = head[:len(head)-1]
	}
	return len(head) > 0
}
