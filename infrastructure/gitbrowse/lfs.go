package gitbrowse

import (
	"io"
	"regexp"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// LFSPointer is a Git LFS pointer file's object. Present: the server can
// serve its content.
type LFSPointer struct {
	Oid     string `json:"oid"`
	Size    int64  `json:"size"`
	Present bool   `json:"present"`
}

// LFSStore serves pointers' content (for one reader of one repository).
type LFSStore interface {
	Present(oid string, size int64) bool
	Open(oid string, size int64) (io.ReadCloser, error)
}

// maxPointerSize: pointer files are smaller than 1024 bytes (LFS spec).
const maxPointerSize = 1024

// pointerPattern is the pointer format git-lfs writes: version, extensions,
// oid and size, in that order.
var pointerPattern = regexp.MustCompile(`\Aversion https://git-lfs\.github\.com/spec/v1\n(?:ext-[0-9]+-[a-z0-9]+ sha256:[0-9a-f]{64}\n)*oid sha256:([0-9a-f]{64})\nsize ([0-9]{1,18})\n\z`)

// ParsePointer recognizes a Git LFS pointer file.
func ParsePointer(data []byte) (*LFSPointer, bool) {
	if len(data) >= maxPointerSize {
		return nil, false
	}
	m := pointerPattern.FindSubmatch(data)
	if m == nil {
		return nil, false
	}
	n, err := strconv.ParseInt(string(m[2]), 10, 64)
	if err != nil {
		return nil, false
	}
	return &LFSPointer{Oid: string(m[1]), Size: n}, true
}

// FilePointer returns the pointer in a (regular) file, if it is one.
func FilePointer(f *object.File) (*LFSPointer, bool) {
	if f.Size >= maxPointerSize || (f.Mode != filemode.Regular && f.Mode != filemode.Executable) {
		return nil, false
	}
	r, err := f.Reader()
	if err != nil {
		return nil, false
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxPointerSize))
	if err != nil {
		return nil, false
	}
	return ParsePointer(data)
}
