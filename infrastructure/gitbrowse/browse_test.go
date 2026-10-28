package gitbrowse

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	t    *testing.T
	repo *git.Repository
	st   *memory.Storage
	wt   *git.Worktree
	n    int
}

func newFixture(t *testing.T) *fixture {
	st := memory.NewStorage()
	repo, err := git.Init(st, memfs.New())
	require.NoError(t, err)
	require.NoError(t, repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	return &fixture{t: t, repo: repo, st: st, wt: wt}
}

// commit writes files (content "" deletes), commits, and returns the hash.
func (f *fixture) commit(msg string, files map[string]string) plumbing.Hash {
	f.t.Helper()
	for name, content := range files {
		if content == "" {
			_, err := f.wt.Remove(name)
			require.NoError(f.t, err)
			continue
		}
		if dir := name[:max(0, strings.LastIndex(name, "/"))]; dir != "" {
			require.NoError(f.t, f.wt.Filesystem.MkdirAll(dir, 0o755))
		}
		w, err := f.wt.Filesystem.Create(name)
		require.NoError(f.t, err)
		_, _ = w.Write([]byte(content))
		require.NoError(f.t, w.Close())
		_, err = f.wt.Add(name)
		require.NoError(f.t, err)
	}
	f.n++
	when := time.Date(2026, 1, 1, 0, f.n, 0, 0, time.UTC)
	h, err := f.wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "Ann", Email: "ann@example.com", When: when}})
	require.NoError(f.t, err)
	return h
}

func (f *fixture) branch(name string) {
	f.t.Helper()
	require.NoError(f.t, f.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: true}))
}

func (f *fixture) checkout(name string) {
	f.t.Helper()
	require.NoError(f.t, f.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name)}))
}

func TestRefsAndResolution(t *testing.T) {
	f := newFixture(t)
	first := f.commit("first", map[string]string{"README.md": "# hi\n", "src/a.go": "package a\n"})
	f.branch("feature/x")
	f.commit("on feature", map[string]string{"src/b.go": "package a\n"})
	f.checkout("main")
	_, err := f.repo.CreateTag("v1", first, &git.CreateTagOptions{Message: "release", Tagger: &object.Signature{Name: "Ann", When: time.Now()}})
	require.NoError(t, err)

	def, branches, tags, err := Refs(f.st)
	require.NoError(t, err)
	assert.Equal(t, "main", def)
	assert.Equal(t, []string{"feature/x", "main"}, []string{branches[0].Name, branches[1].Name}, "newest first")
	require.Len(t, tags, 1)
	assert.Equal(t, first.String(), tags[0].Commit.Hash, "annotated tags are peeled")

	for spec, want := range map[string][2]string{
		"main":                  {"main", ""},
		"main/src/a.go":         {"main", "src/a.go"},
		"feature/x/src/b.go":    {"feature/x", "src/b.go"},
		"v1/README.md":          {"v1", "README.md"},
		first.String() + "/src": {first.String(), "src"},
	} {
		ref, path, c, err := SplitRefPath(f.st, spec)
		require.NoError(t, err, spec)
		assert.Equal(t, want, [2]string{ref, path}, spec)
		assert.NotNil(t, c)
	}
	_, _, _, err = SplitRefPath(f.st, "nope/README.md")
	assert.ErrorIs(t, err, ErrRefNotFound)
	_, err = ResolveRef(f.st, first.String()[:7])
	assert.ErrorIs(t, err, ErrRefNotFound, "only full hashes")
}

func TestTreeAndFiles(t *testing.T) {
	f := newFixture(t)
	big := strings.Repeat("x", 2000)
	f.commit("files", map[string]string{"README.md": "# hi\n", "b.txt": "b", "A.txt": "a", "zdir/c.txt": "c", "bin.dat": "a\x00b", "big.txt": big})
	c, err := ResolveRef(f.st, "main")
	require.NoError(t, err)

	entries, err := Tree(c, "")
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"zdir", "A.txt", "b.txt", "big.txt", "bin.dat", "README.md"}, names, "directories first, then by name")
	assert.Equal(t, "README.md", Readme(entries).Name)
	assert.Equal(t, int64(1), entries[1].Size)
	sub, err := Tree(c, "zdir")
	require.NoError(t, err)
	assert.Equal(t, "zdir/c.txt", sub[0].Path)
	_, err = Tree(c, "missing")
	assert.ErrorIs(t, err, ErrPathNotFound)

	file, err := ReadFile(c, "README.md", 1000)
	require.NoError(t, err)
	assert.Equal(t, "# hi\n", file.Content)
	file, err = ReadFile(c, "bin.dat", 1000)
	require.NoError(t, err)
	assert.True(t, file.Binary)
	assert.Empty(t, file.Content)
	file, err = ReadFile(c, "big.txt", 100)
	require.NoError(t, err)
	assert.True(t, file.Truncated)
	assert.Len(t, file.Content, 100)
	assert.Equal(t, int64(2000), file.Size)
	_, err = ReadFile(c, "zdir", 100)
	assert.ErrorIs(t, err, ErrPathNotFound)
}

func TestIsBinary(t *testing.T) {
	assert.False(t, IsBinary([]byte("héllo")))
	assert.True(t, IsBinary([]byte("a\x00b")))
	assert.True(t, IsBinary([]byte{0xff, 0xfe, 'a', 'b', 'c', 'd'}))
	cut := append(bytes.Repeat([]byte("a"), 7999), "é"...)[:8000] // the cut splits é
	assert.False(t, IsBinary(append(cut, "rest"...)))
}

func TestLog(t *testing.T) {
	f := newFixture(t)
	for i := range 5 {
		files := map[string]string{"all.txt": fmt.Sprint(i)}
		if i%2 == 0 {
			files["even.txt"] = fmt.Sprint(i)
		}
		f.commit(fmt.Sprintf("c%d", i), files)
	}
	c, err := ResolveRef(f.st, "main")
	require.NoError(t, err)

	commits, more, err := Log(c, "", 1, 2)
	require.NoError(t, err)
	assert.True(t, more)
	assert.Equal(t, []string{"c3", "c2"}, []string{commits[0].Message, commits[1].Message})

	commits, more, err = Log(c, "even.txt", 0, 10)
	require.NoError(t, err)
	assert.False(t, more)
	var msgs []string
	for _, c := range commits {
		msgs = append(msgs, c.Message)
	}
	assert.Equal(t, []string{"c4", "c2", "c0"}, msgs, "only commits changing the path")
}

func TestDiff(t *testing.T) {
	f := newFixture(t)
	lines := func(n int, change map[int]string) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			if s, ok := change[i]; ok {
				b.WriteString(s + "\n")
			} else {
				fmt.Fprintf(&b, "line %d\n", i)
			}
		}
		return b.String()
	}
	before := f.commit("before", map[string]string{"a.txt": lines(20, nil), "gone.txt": "bye\n", "old-name.txt": strings.Repeat("same content\n", 10), "img.bin": "\x00\x01"})
	after := f.commit("after", map[string]string{"a.txt": lines(20, map[int]string{10: "changed"}), "gone.txt": "", "old-name.txt": "", "new-name.txt": strings.Repeat("same content\n", 10), "new.txt": "hello\n", "img.bin": "\x00\x02"})
	from, _ := GetCommit(f.st, before.String())
	to, _ := GetCommit(f.st, after.String())

	d, err := DiffCommits(from, to, DefaultLimits)
	require.NoError(t, err)
	byPath := map[string]FileDiff{}
	for _, fd := range d.Files {
		byPath[fd.Path] = fd
	}
	assert.Equal(t, "deleted", byPath["gone.txt"].Status)
	assert.Equal(t, "added", byPath["new.txt"].Status)
	assert.Equal(t, "renamed", byPath["new-name.txt"].Status)
	assert.Equal(t, "old-name.txt", byPath["new-name.txt"].OldPath)
	assert.True(t, byPath["img.bin"].Binary)

	a := byPath["a.txt"]
	assert.Equal(t, "modified", a.Status)
	assert.Equal(t, 1, a.Additions)
	assert.Equal(t, 1, a.Deletions)
	require.Len(t, a.Hunks, 1)
	h := a.Hunks[0]
	assert.Equal(t, [4]int{7, 7, 7, 7}, [4]int{h.OldStart, h.OldLines, h.NewStart, h.NewLines}, "3 lines of context around line 10")
	assert.Equal(t, Line{Kind: "-", Text: "line 10", Old: 10}, h.Lines[3])
	assert.Equal(t, Line{Kind: "+", Text: "changed", New: 10}, h.Lines[4])
	assert.Equal(t, Line{Kind: " ", Text: "line 13", Old: 13, New: 13}, h.Lines[7])
	assert.Len(t, h.Lines, 8)
	assert.Equal(t, 2, d.Additions, "a.txt and new.txt; the rename and the binary add no lines")
	assert.Equal(t, 2, d.Deletions, "a.txt and gone.txt")

	tight, err := DiffCommits(from, to, Limits{MaxFiles: 1, MaxLinesPerFile: 3000, MaxTotalLines: 20000, Context: 3})
	require.NoError(t, err)
	assert.True(t, tight.Truncated)
	assert.Len(t, tight.Files, len(d.Files), "every file is listed")

	root, err := DiffCommits(nil, from, DefaultLimits)
	require.NoError(t, err)
	assert.Len(t, root.Files, 4, "a root commit adds everything")
}

func TestCompare(t *testing.T) {
	f := newFixture(t)
	f.commit("base", map[string]string{"a.txt": "a\n"})
	f.branch("topic")
	f.commit("t1", map[string]string{"t.txt": "1\n"})
	f.commit("t2", map[string]string{"t.txt": "2\n"})
	f.checkout("main")
	f.commit("main moves on", map[string]string{"m.txt": "m\n"})

	base, _ := ResolveRef(f.st, "main")
	head, _ := ResolveRef(f.st, "topic")
	cmp, err := Compare(base, head, DefaultLimits)
	require.NoError(t, err)
	assert.Equal(t, []string{"t1", "t2"}, []string{cmp.Commits[0].Message, cmp.Commits[1].Message}, "oldest first, only the topic's")
	require.Len(t, cmp.Diff.Files, 1, "main's own change is not part of it")
	assert.Equal(t, "t.txt", cmp.Diff.Files[0].Path)
	assert.NotEmpty(t, cmp.MergeBase)
}

func TestBlame(t *testing.T) {
	f := newFixture(t)
	first := f.commit("one", map[string]string{"a.txt": "x\ny\n"})
	second := f.commit("two", map[string]string{"a.txt": "x\nz\n"})
	c, _ := ResolveRef(f.st, "main")
	lines, err := Blame(c, "a.txt", 1000)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	assert.Equal(t, first.String(), lines[0].Hash)
	assert.Equal(t, second.String(), lines[1].Hash)
	assert.Equal(t, "Ann", lines[0].Author)
	_, err = Blame(c, "a.txt", 2)
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestArchive(t *testing.T) {
	f := newFixture(t)
	f.commit("files", map[string]string{"README.md": "hi\n", "dir/a.txt": "a\n"})
	c, _ := ResolveRef(f.st, "main")

	var zbuf bytes.Buffer
	require.NoError(t, WriteArchive(&zbuf, c, Zip, "repo-main", nil))
	zr, err := zip.NewReader(bytes.NewReader(zbuf.Bytes()), int64(zbuf.Len()))
	require.NoError(t, err)
	got := map[string]string{}
	for _, file := range zr.File {
		r, _ := file.Open()
		data, _ := io.ReadAll(r)
		got[file.Name] = string(data)
	}
	assert.Equal(t, map[string]string{"repo-main/README.md": "hi\n", "repo-main/dir/a.txt": "a\n"}, got)

	var tbuf bytes.Buffer
	require.NoError(t, WriteArchive(&tbuf, c, TarGz, "repo-main", nil))
	gz, err := gzip.NewReader(&tbuf)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	got = map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		data, _ := io.ReadAll(tr)
		got[h.Name] = string(data)
	}
	assert.Equal(t, map[string]string{"repo-main/README.md": "hi\n", "repo-main/dir/a.txt": "a\n"}, got)
	assert.ErrorIs(t, WriteArchive(io.Discard, c, "rar", "x", nil), ErrFormat)
}
