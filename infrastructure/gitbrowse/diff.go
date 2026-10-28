package gitbrowse

import (
	"context"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Limits bound a diff's size.
type Limits struct {
	MaxFiles        int // files with content shown; the rest are listed only
	MaxLinesPerFile int
	MaxTotalLines   int
	Context         int // unchanged lines around changes
}

var DefaultLimits = Limits{MaxFiles: 300, MaxLinesPerFile: 3000, MaxTotalLines: 20000, Context: 3}

// Diff is a set of changed files.
type Diff struct {
	Files     []FileDiff `json:"files"`
	Additions int        `json:"additions"`
	Deletions int        `json:"deletions"`
	// Truncated: some files are listed without their content.
	Truncated bool `json:"truncated"`
}

type FileDiff struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"` // renames
	Status    string `json:"status"`             // added, deleted, modified, renamed
	Binary    bool   `json:"binary"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Hunks     []Hunk `json:"hunks"`
	// Truncated: too large to show (counts are still right).
	Truncated bool `json:"truncated"`
}

type Hunk struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Lines    []Line `json:"lines"`
}

type Line struct {
	Kind string `json:"kind"` // " ", "+" or "-"
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}

var renameOptions = &object.DiffTreeOptions{DetectRenames: true, RenameScore: 60, RenameLimit: 100}

// DiffCommits diffs from "from" to "to"; a nil from is the empty tree (a
// root commit).
func DiffCommits(from, to *object.Commit, limits Limits) (*Diff, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var fromTree *object.Tree
	if from != nil {
		t, err := from.Tree()
		if err != nil {
			return nil, err
		}
		fromTree = t
	}
	toTree, err := to.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := object.DiffTreeWithOptions(ctx, fromTree, toTree, renameOptions)
	if err != nil {
		return nil, err
	}

	out := &Diff{Files: []FileDiff{}}
	totalLines := 0
	for _, ch := range changes {
		fd := FileDiff{Path: ch.To.Name, Status: "modified"}
		switch {
		case ch.From.Name == "":
			fd.Status = "added"
		case ch.To.Name == "":
			fd.Status, fd.Path = "deleted", ch.From.Name
		case ch.From.Name != ch.To.Name:
			fd.Status, fd.OldPath = "renamed", ch.From.Name
		}
		patch, err := ch.PatchContext(ctx)
		if err != nil {
			return nil, err
		}
		for _, fp := range patch.FilePatches() {
			if fp.IsBinary() {
				fd.Binary = true
				continue
			}
			hunks, adds, dels := buildHunks(fp.Chunks(), limits.Context)
			fd.Additions, fd.Deletions = adds, dels
			lines := 0
			for _, h := range hunks {
				lines += len(h.Lines)
			}
			if len(out.Files) >= limits.MaxFiles || lines > limits.MaxLinesPerFile || totalLines+lines > limits.MaxTotalLines {
				fd.Truncated = true
				out.Truncated = true
			} else {
				fd.Hunks = hunks
				totalLines += lines
			}
		}
		if fd.Hunks == nil {
			fd.Hunks = []Hunk{}
		}
		out.Additions += fd.Additions
		out.Deletions += fd.Deletions
		out.Files = append(out.Files, fd)
	}
	return out, nil
}

// buildHunks turns go-git's chunks (runs of equal, added and deleted text)
// into unified-diff hunks with ctx lines of context.
func buildHunks(chunks []diff.Chunk, ctx int) ([]Hunk, int, int) {
	type line struct {
		kind     string
		text     string
		old, new int
	}
	var all []line
	oldN, newN, adds, dels := 1, 1, 0, 0
	for _, ch := range chunks {
		for _, text := range splitLines(ch.Content()) {
			switch ch.Type() {
			case diff.Equal:
				all = append(all, line{" ", text, oldN, newN})
				oldN++
				newN++
			case diff.Add:
				all = append(all, line{"+", text, 0, newN})
				newN++
				adds++
			case diff.Delete:
				all = append(all, line{"-", text, oldN, 0})
				oldN++
				dels++
			}
		}
	}

	// Mark the lines to show: changes and ctx lines around them.
	show := make([]bool, len(all))
	for i, l := range all {
		if l.kind != " " {
			for j := max(0, i-ctx); j <= min(len(all)-1, i+ctx); j++ {
				show[j] = true
			}
		}
	}
	var hunks []Hunk
	for i := 0; i < len(all); {
		if !show[i] {
			i++
			continue
		}
		h := Hunk{Lines: []Line{}}
		for ; i < len(all) && show[i]; i++ {
			l := all[i]
			if h.OldStart == 0 && l.old != 0 {
				h.OldStart = l.old
			}
			if h.NewStart == 0 && l.new != 0 {
				h.NewStart = l.new
			}
			if l.kind != "+" {
				h.OldLines++
			}
			if l.kind != "-" {
				h.NewLines++
			}
			h.Lines = append(h.Lines, Line{Kind: l.kind, Text: l.text, Old: l.old, New: l.new})
		}
		hunks = append(hunks, h)
	}
	return hunks, adds, dels
}

// splitLines splits text into lines without their newline; a final newline
// does not make an extra empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
