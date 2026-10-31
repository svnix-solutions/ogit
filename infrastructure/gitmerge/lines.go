package gitmerge

import (
	"bytes"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Why a path conflicts.
const (
	ConflictContent  = "content"   // the same lines changed on both sides
	ConflictBinary   = "binary"    // a binary file changed on both sides
	ConflictTooLarge = "too_large" // over MaxLineMergeSize, changed on both sides
	ConflictDeleted  = "deleted"   // changed on one side, deleted on the other
	ConflictAdded    = "added"     // added on both sides, differently
	ConflictType     = "type"      // a file against a directory, or kinds that don't merge (symlinks, submodules)
)

const (
	maxConflictHunks  = 20   // per file
	maxConflictLines  = 200  // per side of a hunk
	maxDiffEdits      = 2048 // past that, diffLines calls the range rewritten
	binaryProbeLength = 8000 // as git: a NUL in the first 8000 bytes means binary
)

// MaxLineMergeSize is the largest file merged line by line, on any side.
// Merges run on every view of a pull request, so they have to stay cheap.
const MaxLineMergeSize = 1 << 20

// ConflictFile is a path the merge can't resolve. Hunks are only set for
// ConflictContent, and Truncated says some were left out.
type ConflictFile struct {
	Path      string
	Reason    string
	Hunks     []ConflictHunk
	Truncated bool
}

// ConflictHunk is one conflicting range. Ours and Theirs are the lines of
// each side, and the Line fields their first line, counted from 1.
type ConflictHunk struct {
	OursLine, TheirsLine int
	Ours, Theirs         string
}

// mergeFile merges a path changed on both sides. It returns the merged entry
// or, with a nil entry, the conflict.
func (w *writer) mergeFile(path string, be entry, inB bool, oe entry, inO bool, te entry, inT bool) (*entry, *ConflictFile, error) {
	conflict := func(reason string) (*entry, *ConflictFile, error) {
		return nil, &ConflictFile{Path: path, Reason: reason}, nil
	}
	switch {
	case !inO || !inT:
		return conflict(ConflictDeleted)
	case !inB:
		return conflict(ConflictAdded)
	case !lineMergeable(be.mode) || !lineMergeable(oe.mode) || !lineMergeable(te.mode):
		return conflict(ConflictType)
	}
	mode := oe.mode
	if oe.mode == be.mode {
		mode = te.mode
	} else if te.mode != be.mode && te.mode != oe.mode {
		return conflict(ConflictType)
	}
	if oe.hash == te.hash || oe.hash == be.hash || te.hash == be.hash { // only the modes differ
		hash := oe.hash
		if oe.hash == be.hash {
			hash = te.hash
		}
		return &entry{mode, hash}, nil, nil
	}

	sides := make([][]byte, 3)
	for i, e := range []entry{be, oe, te} {
		blob, err := object.GetBlob(w, e.hash)
		if err != nil {
			return nil, nil, err
		}
		if blob.Size > MaxLineMergeSize {
			return conflict(ConflictTooLarge)
		}
		r, err := blob.Reader()
		if err != nil {
			return nil, nil, err
		}
		sides[i], err = io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	for _, s := range sides {
		if bytes.IndexByte(s[:min(len(s), binaryProbeLength)], 0) >= 0 {
			return conflict(ConflictBinary)
		}
	}
	merged, hunks := mergeLines(sides[0], sides[1], sides[2])
	if len(hunks) > 0 {
		return nil, conflictFile(path, hunks), nil
	}
	obj := w.out.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	content, err := obj.Writer()
	if err != nil {
		return nil, nil, err
	}
	if _, err := content.Write(merged); err != nil {
		return nil, nil, err
	}
	if err := content.Close(); err != nil {
		return nil, nil, err
	}
	hash, err := w.out.SetEncodedObject(obj)
	if err != nil {
		return nil, nil, err
	}
	return &entry{mode, hash}, nil, nil
}

// conflictFile applies the limits on what a conflict shows.
func conflictFile(path string, hunks []ConflictHunk) *ConflictFile {
	file := &ConflictFile{Path: path, Reason: ConflictContent, Hunks: hunks}
	if len(hunks) > maxConflictHunks {
		file.Hunks, file.Truncated = hunks[:maxConflictHunks], true
	}
	for i := range file.Hunks {
		h := &file.Hunks[i]
		var oursCut, theirsCut bool
		h.Ours, oursCut = firstLines(h.Ours)
		h.Theirs, theirsCut = firstLines(h.Theirs)
		file.Truncated = file.Truncated || oursCut || theirsCut
	}
	return file
}

func firstLines(text string) (string, bool) {
	end := 0
	for range maxConflictLines {
		n := strings.IndexByte(text[end:], '\n')
		if n < 0 {
			return text, false
		}
		end += n + 1
	}
	return text[:end], end < len(text)
}

func lineMergeable(m filemode.FileMode) bool {
	return m == filemode.Regular || m == filemode.Deprecated || m == filemode.Executable
}

// mergeLines is a three-way merge of text, the diff3 way. Lines of base kept
// by both sides split the files into ranges; a range only one side changed
// takes that side, and one both changed the same way takes that. Everything
// else conflicts, adjacent changes included (as in git). A conflict's common
// first and last lines are not part of it.
func mergeLines(base, ours, theirs []byte) ([]byte, []ConflictHunk) {
	b, o, t := splitLines(base), splitLines(ours), splitLines(theirs)
	ids := map[string]int{}
	bi, oi, ti := intern(b, ids), intern(o, ids), intern(t, ids)
	toOurs, toTheirs := diffLines(bi, oi), diffLines(bi, ti)

	var merged []byte
	var hunks []ConflictHunk
	emit := func(lines []string) {
		for _, l := range lines {
			merged = append(merged, l...)
		}
	}
	// merge resolves the range up to, excluding, the given lines.
	var bs, os, ts int
	merge := func(be, oe, te int) {
		br, or, tr := bi[bs:be], oi[os:oe], ti[ts:te]
		switch {
		case equal(or, br):
			emit(t[ts:te])
		case equal(tr, br) || equal(or, tr):
			emit(o[os:oe])
		default:
			prefix := 0
			for prefix < len(or) && prefix < len(tr) && or[prefix] == tr[prefix] {
				prefix++
			}
			suffix := 0
			for suffix < len(or)-prefix && suffix < len(tr)-prefix && or[len(or)-1-suffix] == tr[len(tr)-1-suffix] {
				suffix++
			}
			hunks = append(hunks, ConflictHunk{
				OursLine: os + prefix + 1, TheirsLine: ts + prefix + 1,
				Ours: strings.Join(o[os+prefix:oe-suffix], ""), Theirs: strings.Join(t[ts+prefix:te-suffix], ""),
			})
		}
	}
	for i := range b {
		oLine, tLine := toOurs[i], toTheirs[i]
		if oLine < 0 || tLine < 0 {
			continue
		}
		merge(i, oLine, tLine)
		merged = append(merged, b[i]...)
		bs, os, ts = i+1, oLine+1, tLine+1
	}
	merge(len(b), len(o), len(t))
	return merged, hunks
}

// splitLines keeps each line's terminator, so joining them gives the input.
func splitLines(data []byte) []string {
	var lines []string
	for len(data) > 0 {
		n := bytes.IndexByte(data, '\n') + 1
		if n == 0 {
			n = len(data)
		}
		lines = append(lines, string(data[:n]))
		data = data[n:]
	}
	return lines
}

func intern(lines []string, ids map[string]int) []int {
	out := make([]int, len(lines))
	for i, l := range lines {
		id, ok := ids[l]
		if !ok {
			id = len(ids)
			ids[l] = id
		}
		out[i] = id
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffLines is a Myers diff: for each line of a, the line of b it matches,
// or -1. Matches never cross. Ranges needing over maxDiffEdits edits count as
// rewritten, which at worst makes a conflict of what could have merged.
func diffLines(a, b []int) []int {
	match := make([]int, len(a))
	for i := range match {
		match[i] = -1
	}
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		match[start] = start
		start++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > start && bEnd > start && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
		match[aEnd] = bEnd
	}
	x, y := a[start:aEnd], b[start:bEnd]
	n, m := len(x), len(y)
	limit := min(n+m, maxDiffEdits)
	offset := limit + 1
	v := make([]int32, 2*limit+3)
	// trace[d] is v before edit d, for k in -d-1..d+1.
	var trace [][]int32
	takesDown := func(v []int32, center, k, d int) bool {
		return k == -d || (k != d && v[center+k-1] < v[center+k+1])
	}
	for d := 0; d <= limit; d++ {
		trace = append(trace, append([]int32(nil), v[offset-d-1:offset+d+2]...))
		for k := -d; k <= d; k += 2 {
			var i int
			if takesDown(v, offset, k, d) {
				i = int(v[offset+k+1])
			} else {
				i = int(v[offset+k-1]) + 1
			}
			j := i - k
			for i < n && j < m && x[i] == y[j] {
				i++
				j++
			}
			v[offset+k] = int32(i)
			if i < n || j < m {
				continue
			}
			// Done: walk back through the edits, recording the diagonals.
			for e := d; e > 0; e-- {
				k := i - j
				prevK := k - 1
				if takesDown(trace[e], e+1, k, e) {
					prevK = k + 1
				}
				prevI := int(trace[e][e+1+prevK])
				prevJ := prevI - prevK
				for i > prevI && j > prevJ {
					i--
					j--
					match[start+i] = start + j
				}
				i, j = prevI, prevJ
			}
			for i > 0 {
				i--
				match[start+i] = start + i
			}
			return match
		}
	}
	return match
}
