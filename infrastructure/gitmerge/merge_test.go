package gitmerge

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repo struct {
	t  *testing.T
	st storage.Storer
	g  *git.Repository
	wt *git.Worktree
	n  int
}

func newRepo(t *testing.T, st storage.Storer) *repo {
	g, err := git.Init(st, memfs.New())
	require.NoError(t, err)
	require.NoError(t, st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, "refs/heads/main")))
	wt, err := g.Worktree()
	require.NoError(t, err)
	return &repo{t: t, st: st, g: g, wt: wt}
}

// commit writes files ("" deletes) and commits.
func (r *repo) commit(msg string, files map[string]string) *object.Commit {
	r.t.Helper()
	for name, content := range files {
		if content == "" {
			_, err := r.wt.Remove(name)
			require.NoError(r.t, err)
			continue
		}
		if i := strings.LastIndex(name, "/"); i > 0 {
			require.NoError(r.t, r.wt.Filesystem.MkdirAll(name[:i], 0o755))
		}
		f, err := r.wt.Filesystem.Create(name)
		require.NoError(r.t, err)
		_, _ = f.Write([]byte(content))
		require.NoError(r.t, f.Close())
		_, err = r.wt.Add(name)
		require.NoError(r.t, err)
	}
	r.n++
	h, err := r.wt.Commit(msg, &git.CommitOptions{Author: &object.Signature{Name: "Dev", Email: "dev@example.com", When: time.Date(2026, 1, 1, 0, r.n, 0, 0, time.UTC)}})
	require.NoError(r.t, err)
	c, err := object.GetCommit(r.st, h)
	require.NoError(r.t, err)
	return c
}

func (r *repo) branch(name string, create bool) {
	r.t.Helper()
	require.NoError(r.t, r.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(name), Create: create, Force: true}))
}

func (r *repo) head(name string) *object.Commit {
	ref, err := r.st.Reference(plumbing.NewBranchReferenceName(name))
	require.NoError(r.t, err)
	c, err := object.GetCommit(r.st, ref.Hash())
	require.NoError(r.t, err)
	return c
}

// files reads every file of a commit (from the repository plus res).
func files(t *testing.T, st storer.EncodedObjectStorer, res *Result, h plumbing.Hash) map[string]string {
	t.Helper()
	w := &writer{EncodedObjectStorer: st, out: memory.NewStorage()}
	if res != nil {
		w.out = res.objects
	}
	c, err := object.GetCommit(w, h)
	require.NoError(t, err)
	tree, err := object.GetTree(w, c.TreeHash)
	require.NoError(t, err)
	out := map[string]string{}
	require.NoError(t, tree.Files().ForEach(func(f *object.File) error {
		s, err := f.Contents()
		out[f.Name] = s
		return err
	}))
	return out
}

var merger = object.Signature{Name: "Maintainer", Email: "m@example.com", When: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)}

// diverged: main changes a.txt, topic changes b.txt in two commits.
func diverged(t *testing.T) *repo { return divergedIn(t, memory.NewStorage()) }

func divergedIn(t *testing.T, st storage.Storer) *repo {
	r := newRepo(t, st)
	r.commit("base", map[string]string{"a.txt": "a\n", "b.txt": "b\n", "dir/c.txt": "c\n"})
	r.branch("topic", true)
	r.commit("t1", map[string]string{"b.txt": "b1\n"})
	r.commit("t2", map[string]string{"dir/new.txt": "new\n"})
	r.branch("main", false)
	r.commit("m1", map[string]string{"a.txt": "a2\n"})
	return r
}

var mergedFiles = map[string]string{"a.txt": "a2\n", "b.txt": "b1\n", "dir/c.txt": "c\n", "dir/new.txt": "new\n"}

func TestMergeMethods(t *testing.T) {
	for _, method := range []string{MethodMerge, MethodSquash, MethodRebase} {
		t.Run(method, func(t *testing.T) {
			r := diverged(t)
			base, head := r.head("main"), r.head("topic")
			res, err := Merge(r.st, Request{Base: base, Head: head, Method: method, Message: "Merge topic", Committer: merger})
			require.NoError(t, err)
			assert.Equal(t, mergedFiles, files(t, r.st, res, res.Commit))

			w := &writer{EncodedObjectStorer: r.st, out: res.objects}
			c, err := object.GetCommit(w, res.Commit)
			require.NoError(t, err)
			switch method {
			case MethodMerge:
				assert.Equal(t, []plumbing.Hash{base.Hash, head.Hash}, c.ParentHashes)
				assert.Equal(t, "Merge topic", c.Message)
				assert.Equal(t, "Maintainer", c.Author.Name)
			case MethodSquash:
				assert.Equal(t, []plumbing.Hash{base.Hash}, c.ParentHashes)
			case MethodRebase:
				assert.Equal(t, "t2", c.Message, "commits keep their messages")
				assert.Equal(t, "Dev", c.Author.Name, "and authors")
				assert.Equal(t, "Maintainer", c.Committer.Name)
				first, err := c.Parent(0)
				require.NoError(t, err)
				assert.Equal(t, "t1", first.Message)
				assert.Equal(t, []plumbing.Hash{base.Hash}, first.ParentHashes, "replayed on top of main")
			}
		})
	}
}

// The merged tree is byte-identical to the one git makes for those files,
// so entries are ordered right (e.g. "dir" vs "dir.txt").
func TestTreesMatchGit(t *testing.T) {
	r := newRepo(t, memory.NewStorage())
	r.commit("base", map[string]string{"dir/a": "1", "dir.txt": "2", "dir-x": "3"})
	r.branch("topic", true)
	r.commit("t", map[string]string{"dir/b": "4"})
	r.branch("main", false)
	r.commit("m", map[string]string{"z": "5"})
	res, err := Merge(r.st, Request{Base: r.head("main"), Head: r.head("topic"), Method: MethodSquash, Committer: merger})
	require.NoError(t, err)
	w := &writer{EncodedObjectStorer: r.st, out: res.objects}
	merged, err := object.GetCommit(w, res.Commit)
	require.NoError(t, err)

	expected := r.commit("same files by git", map[string]string{"dir/b": "4"})
	assert.Equal(t, expected.TreeHash, merged.TreeHash)
}

func TestConflicts(t *testing.T) {
	setup := func(t *testing.T, main, topic map[string]string) (*repo, *object.Commit, *object.Commit) {
		r := newRepo(t, memory.NewStorage())
		r.commit("base", map[string]string{"a.txt": "a\n", "gone.txt": "g\n"})
		r.branch("topic", true)
		r.commit("t", topic)
		r.branch("main", false)
		r.commit("m", main)
		return r, r.head("main"), r.head("topic")
	}
	for name, tc := range map[string]struct {
		main, topic map[string]string
		conflicts   []string
	}{
		"both_edit":            {map[string]string{"a.txt": "main\n"}, map[string]string{"a.txt": "topic\n"}, []string{"a.txt"}},
		"same_edit":            {map[string]string{"a.txt": "same\n"}, map[string]string{"a.txt": "same\n"}, nil},
		"edit_vs_delete":       {map[string]string{"gone.txt": "edited\n"}, map[string]string{"gone.txt": ""}, []string{"gone.txt"}},
		"both_delete":          {map[string]string{"gone.txt": ""}, map[string]string{"gone.txt": ""}, nil},
		"both_add_differently": {map[string]string{"new.txt": "1\n"}, map[string]string{"new.txt": "2\n"}, []string{"new.txt"}},
		"file_vs_directory":    {map[string]string{"x": "file\n"}, map[string]string{"x/y": "in a dir\n"}, []string{"x"}},
	} {
		t.Run(name, func(t *testing.T) {
			r, base, head := setup(t, tc.main, tc.topic)
			conflicts, upToDate, err := Check(r.st, base, head)
			require.NoError(t, err)
			assert.False(t, upToDate)
			assert.Equal(t, tc.conflicts, conflicts)
			_, err = Merge(r.st, Request{Base: base, Head: head, Method: MethodMerge, Committer: merger})
			var conflict *ConflictError
			if tc.conflicts == nil {
				assert.NoError(t, err)
			} else {
				require.True(t, errors.As(err, &conflict), "%v", err)
				assert.Equal(t, tc.conflicts, conflict.Paths)
			}
		})
	}
}

func TestNothingToMergeAndRebaseLimits(t *testing.T) {
	r := diverged(t)
	main := r.head("main")
	topic := r.head("topic")

	_, upToDate, err := Check(r.st, main, main)
	require.NoError(t, err)
	assert.True(t, upToDate)
	_, err = Merge(r.st, Request{Base: main, Head: main, Method: MethodMerge, Committer: merger})
	assert.ErrorIs(t, err, ErrNothingToMerge)
	_, err = Merge(r.st, Request{Base: main, Head: topic, Method: "octopus", Committer: merger})
	assert.ErrorIs(t, err, ErrMethod)

	// A topic containing a merge commit can't be rebased.
	r.branch("topic", false)
	mergeCommit, err := r.wt.Commit("merge main into topic", &git.CommitOptions{
		Author: &merger, Parents: []plumbing.Hash{topic.Hash, main.Hash}, AllowEmptyCommits: true,
	})
	require.NoError(t, err)
	head, err := object.GetCommit(r.st, mergeCommit)
	require.NoError(t, err)
	_, err = Merge(r.st, Request{Base: main, Head: head, Method: MethodRebase, Committer: merger})
	assert.ErrorIs(t, err, ErrRebaseMerges)
}

// rootCommit stores a parentless commit with one file, outside any branch.
func rootCommit(t *testing.T, st storer.EncodedObjectStorer, name, content string) *object.Commit {
	t.Helper()
	blob := st.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	wr, _ := blob.Writer()
	_, _ = wr.Write([]byte(content))
	_ = wr.Close()
	blobHash, err := st.SetEncodedObject(blob)
	require.NoError(t, err)
	w := &writer{EncodedObjectStorer: st, out: memory.NewStorage()}
	tree, err := w.buildTree(map[string]entry{name: {mode: 0o100644, hash: blobHash}})
	require.NoError(t, err)
	treeObj, _ := w.out.EncodedObject(plumbing.TreeObject, tree)
	_, err = st.SetEncodedObject(treeObj)
	require.NoError(t, err)
	obj := st.NewEncodedObject()
	require.NoError(t, (&object.Commit{Author: merger, Committer: merger, Message: "other root", TreeHash: tree}).Encode(obj))
	h, err := st.SetEncodedObject(obj)
	require.NoError(t, err)
	c, err := object.GetCommit(st, h)
	require.NoError(t, err)
	return c
}

func TestUnrelatedHistories(t *testing.T) {
	r := newRepo(t, memory.NewStorage())
	r.commit("main root", map[string]string{"a.txt": "a\n"})
	other := rootCommit(t, r.st, "b.txt", "b\n")

	res, err := Merge(r.st, Request{Base: r.head("main"), Head: other, Method: MethodMerge, Committer: merger})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a.txt": "a\n", "b.txt": "b\n"}, files(t, r.st, res, res.Commit))

	clash := rootCommit(t, r.st, "a.txt", "different\n")
	_, err = Merge(r.st, Request{Base: r.head("main"), Head: clash, Method: MethodMerge, Committer: merger})
	var conflict *ConflictError
	assert.True(t, errors.As(err, &conflict), "both added a.txt")
}

func TestWrite(t *testing.T) {
	for name, st := range map[string]storage.Storer{
		"objects": memory.NewStorage(), // no PackfileWriter: one object at a time
		"pack":    filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault()),
	} {
		t.Run(name, func(t *testing.T) {
			r := divergedIn(t, st)
			res, err := Merge(r.st, Request{Base: r.head("main"), Head: r.head("topic"), Method: MethodMerge, Committer: merger})
			require.NoError(t, err)
			_, err = object.GetCommit(r.st, res.Commit)
			assert.Error(t, err, "nothing is written before Write")
			require.NoError(t, Write(r.st, res))
			assert.Equal(t, mergedFiles, files(t, r.st, nil, res.Commit), "readable from the repository alone")
			if name == "pack" {
				packs, err := r.st.(storer.PackedObjectStorer).ObjectPacks()
				require.NoError(t, err)
				assert.Len(t, packs, 1, "written as one pack")
			}
		})
	}
}

func TestEdit(t *testing.T) {
	st := memory.NewStorage()
	sig := object.Signature{Name: "A", Email: "a@x", When: time.Unix(1700000000, 0)}
	first, err := Edit(st, nil, map[string][]byte{"Home.md": []byte("hi\n"), "docs/Guide.md": []byte("g\n")}, sig, "first")
	require.NoError(t, err)
	require.NoError(t, Write(st, first))
	c1, err := object.GetCommit(st, first.Commit)
	require.NoError(t, err)
	assert.Empty(t, c1.ParentHashes)

	second, err := Edit(st, c1, map[string][]byte{"Home.md": []byte("hello\n"), "docs/Guide.md": nil, "New.md": []byte("n\n")}, sig, "second")
	require.NoError(t, err)
	require.NoError(t, Write(st, second))
	c2, err := object.GetCommit(st, second.Commit)
	require.NoError(t, err)
	assert.Equal(t, []plumbing.Hash{c1.Hash}, c2.ParentHashes)
	assert.Equal(t, map[string]string{"Home.md": "hello\n", "New.md": "n\n"}, files(t, st, second, second.Commit))
}
