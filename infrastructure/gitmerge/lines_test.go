package gitmerge

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lines(from, to int) string {
	var b strings.Builder
	for i := from; i < to; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func TestMergeLines(t *testing.T) {
	base := "a\nb\nc\nd\ne\nf\ng\n"
	for name, tc := range map[string]struct {
		base, ours, theirs string
		merged             string // when clean
		hunks              []ConflictHunk
	}{
		"different_lines":   {base: base, ours: "A\nb\nc\nd\ne\nf\ng\n", theirs: "a\nb\nc\nd\ne\nf\nG\n", merged: "A\nb\nc\nd\ne\nf\nG\n"},
		"insert_and_delete": {base: base, ours: "a\nnew\nb\nc\nd\ne\nf\ng\n", theirs: "a\nb\nc\nd\ne\ng\n", merged: "a\nnew\nb\nc\nd\ne\ng\n"},
		"same_change":       {base: base, ours: "a\nX\nc\nd\ne\nf\ng\n", theirs: "a\nX\nc\nd\ne\nf\nG\n", merged: "a\nX\nc\nd\ne\nf\nG\n"},
		"one_side_empties":  {base: "a\n", ours: "", theirs: "a\n", merged: ""},
		"no_final_newline":  {base: "a\nb\nc\nd", ours: "A\nb\nc\nd", theirs: "a\nb\nc\nD", merged: "A\nb\nc\nD"},
		"crlf_kept":         {base: "a\r\nb\r\nc\r\nd\r\n", ours: "A\r\nb\r\nc\r\nd\r\n", theirs: "a\r\nb\r\nc\r\nD\r\n", merged: "A\r\nb\r\nc\r\nD\r\n"},
		"same_line":         {base: base, ours: "a\nb\nours\nd\ne\nf\ng\n", theirs: "a\nb\ntheirs\nd\ne\nf\ng\n", hunks: []ConflictHunk{{3, 3, "ours\n", "theirs\n"}}},
		"adjacent_lines":    {base: base, ours: "a\nB\nc\nd\ne\nf\ng\n", theirs: "a\nb\nC\nd\ne\nf\ng\n", hunks: []ConflictHunk{{2, 2, "B\nc\n", "b\nC\n"}}},
		"both_insert":       {base: base, ours: "a\nb\nc\nd\none\ne\nf\ng\n", theirs: "a\nb\nc\nd\ntwo\ne\nf\ng\n", hunks: []ConflictHunk{{5, 5, "one\n", "two\n"}}},
		"common_edges":      {base: base, ours: "a\nsame\nours\nend\ne\nf\ng\n", theirs: "a\nsame\ntheirs\nend\ne\nf\ng\n", hunks: []ConflictHunk{{3, 3, "ours\n", "theirs\n"}}},
		"lines_are_shifted": {base: base, ours: "0\n1\na\nb\nc\nd\ne\nf\nG\n", theirs: "a\nb\nc\nX\ne\nf\ng\n", merged: "0\n1\na\nb\nc\nX\ne\nf\nG\n"},
		"two_conflicts": {base: base, ours: "1\nb\nc\nd\ne\nf\n2\n", theirs: "3\nb\nc\nd\ne\nf\n4\n",
			hunks: []ConflictHunk{{1, 1, "1\n", "3\n"}, {7, 7, "2\n", "4\n"}}},
	} {
		t.Run(name, func(t *testing.T) {
			merged, hunks := mergeLines([]byte(tc.base), []byte(tc.ours), []byte(tc.theirs))
			assert.Equal(t, tc.hunks, hunks)
			if tc.hunks == nil {
				assert.Equal(t, tc.merged, string(merged))
			}
		})
	}
}

// Past maxDiffEdits a range counts as rewritten: merges refuse, never lose
// lines.
func TestDiffLimit(t *testing.T) {
	base := lines(0, 3*maxDiffEdits)
	rewritten := strings.ReplaceAll(base, "line", "row")
	_, hunks := mergeLines([]byte(base), []byte(rewritten), []byte("first\n"+base[len("line 0\n"):]))
	assert.Len(t, hunks, 1)
	merged, hunks := mergeLines([]byte(base), []byte(rewritten), []byte(base))
	assert.Empty(t, hunks)
	assert.Equal(t, rewritten, string(merged))
}

func TestDiffLines(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for range 2000 {
		a, b := make([]int, rng.Intn(30)), make([]int, rng.Intn(30))
		for i := range a {
			a[i] = rng.Intn(5)
		}
		for i := range b {
			b[i] = rng.Intn(5)
		}
		last := -1
		for i, j := range diffLines(a, b) {
			if j < 0 {
				continue
			}
			require.Greater(t, j, last, "%v %v", a, b)
			require.Equal(t, a[i], b[j], "%v %v", a, b)
			last = j
		}
	}
}

func TestMergeFiles(t *testing.T) {
	base := lines(0, 20)
	edit := func(old, new string) string { return strings.Replace(base, old, new, 1) }
	merge := func(t *testing.T, method string, main, topic map[string]string) (*repo, *object.Commit, error) {
		r := newRepo(t, memory.NewStorage())
		r.commit("base", map[string]string{"a.txt": base, "big.txt": lines(0, 100000), "bin": "\x00" + base})
		r.branch("topic", true)
		r.commit("t", topic)
		r.branch("main", false)
		r.commit("m", main)
		res, err := Merge(r.st, Request{Base: r.head("main"), Head: r.head("topic"), Method: method, Committer: merger, Message: "merged"})
		if err != nil {
			return r, nil, err
		}
		require.NoError(t, Write(r.st, res))
		c, err := object.GetCommit(r.st, res.Commit)
		require.NoError(t, err)
		return r, c, nil
	}
	read := func(t *testing.T, c *object.Commit, path string) string {
		f, err := c.File(path)
		require.NoError(t, err)
		text, err := f.Contents()
		require.NoError(t, err)
		return text
	}

	for _, method := range []string{MethodMerge, MethodSquash, MethodRebase} {
		t.Run(method, func(t *testing.T) {
			_, c, err := merge(t, method, map[string]string{"a.txt": edit("line 1\n", "main\n")}, map[string]string{"a.txt": edit("line 18\n", "topic\n")})
			require.NoError(t, err)
			assert.Equal(t, strings.Replace(edit("line 1\n", "main\n"), "line 18\n", "topic\n", 1), read(t, c, "a.txt"))
		})
	}

	t.Run("reasons", func(t *testing.T) {
		big := lines(0, 100000)
		_, _, err := merge(t, MethodMerge,
			map[string]string{"a.txt": edit("line 1\n", "main\n"), "big.txt": "0\n" + big, "bin": "\x00main\n" + base},
			map[string]string{"a.txt": edit("line 1\n", "topic\n"), "big.txt": big + "0\n", "bin": "\x00" + base + "topic\n"})
		var conflict *ConflictError
		require.ErrorAs(t, err, &conflict)
		assert.Equal(t, []ConflictFile{
			{Path: "a.txt", Reason: ConflictContent, Hunks: []ConflictHunk{{2, 2, "main\n", "topic\n"}}},
			{Path: "big.txt", Reason: ConflictTooLarge},
			{Path: "bin", Reason: ConflictBinary},
		}, conflict.Files)
	})

	t.Run("hunk limits", func(t *testing.T) {
		file := conflictFile("f", make([]ConflictHunk, 25))
		assert.Len(t, file.Hunks, maxConflictHunks)
		assert.True(t, file.Truncated)

		long := strings.Repeat("x\n", maxConflictLines+1)
		file = conflictFile("f", []ConflictHunk{{Ours: long, Theirs: "y\n"}})
		assert.Equal(t, strings.Repeat("x\n", maxConflictLines), file.Hunks[0].Ours)
		assert.True(t, file.Truncated)
		file = conflictFile("f", []ConflictHunk{{Ours: long[2:], Theirs: "y"}})
		assert.False(t, file.Truncated)
	})

	t.Run("modes", func(t *testing.T) {
		r := newRepo(t, memory.NewStorage())
		r.commit("base", map[string]string{"a.txt": base})
		tree := func(c *object.Commit) map[string]entry {
			w := &writer{EncodedObjectStorer: r.st, out: memory.NewStorage()}
			files, err := w.flatten(c)
			require.NoError(t, err)
			return files
		}
		e := tree(r.head("main"))["a.txt"]
		w := &writer{EncodedObjectStorer: r.st, out: memory.NewStorage()}
		exec := entry{filemode.Executable, e.hash}
		got, conflict, err := w.mergeFile("a.txt", e, true, exec, true, e, true)
		require.NoError(t, err)
		assert.Nil(t, conflict)
		assert.Equal(t, exec, *got)
		got, conflict, err = w.mergeFile("a.txt", e, true, e, true, exec, true)
		require.NoError(t, err)
		assert.Nil(t, conflict)
		assert.Equal(t, exec, *got, "only theirs changed the mode")
		link := entry{filemode.Symlink, e.hash}
		_, conflict, err = w.mergeFile("a.txt", e, true, link, true, exec, true)
		require.NoError(t, err)
		assert.Equal(t, ConflictType, conflict.Reason)
	})
}

// Whenever git and this package both merge cleanly, the result is the same.
// Line alignment can differ, so either may conflict where the other doesn't.
func TestMergeLinesMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	write := func(name string, lines []string) string {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644))
		return path
	}
	rng := rand.New(rand.NewSource(2))
	mutate := func(base []string) []string {
		out := append([]string(nil), base...)
		for range 1 + rng.Intn(3) {
			at := rng.Intn(len(out) + 1)
			switch rng.Intn(3) {
			case 0:
				out = append(out[:at:at], append([]string{fmt.Sprintf("new %d\n", rng.Int())}, out[at:]...)...)
			case 1:
				if at < len(out) {
					out = append(out[:at:at], out[at+1:]...)
				}
			case 2:
				if at < len(out) {
					out[at] = fmt.Sprintf("changed %d\n", rng.Int())
				}
			}
		}
		return out
	}
	both, disagreed := 0, 0
	for range 300 {
		base := make([]string, 30)
		for i := range base {
			base[i] = fmt.Sprintf("%d\n", rng.Intn(20)) // repeated lines on purpose
		}
		ours, theirs := mutate(base), mutate(base)
		bp, op, tp := write("base", base), write("ours", ours), write("theirs", theirs)
		cmd := exec.Command("git", "merge-file", "-p", op, bp, tp)
		out, err := cmd.Output()
		if _, isExit := err.(*exec.ExitError); err != nil && !isExit {
			t.Fatal(err)
		}
		merged, hunks := mergeLines([]byte(strings.Join(base, "")), []byte(strings.Join(ours, "")), []byte(strings.Join(theirs, "")))
		switch {
		case err == nil && len(hunks) == 0:
			both++
			require.Equal(t, string(out), string(merged), "base %q ours %q theirs %q", base, ours, theirs)
		case (err == nil) != (len(hunks) == 0):
			disagreed++
		}
	}
	assert.Greater(t, both, 100)
	assert.Less(t, disagreed, 30, "clean on one side only")
	t.Logf("%d clean on both, %d on one", both, disagreed)
}
