package actrunner

import (
	"embed"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
)

// The runner carries ogit/upload-artifact and ogit/download-artifact:
// GitHub's v4 artifact actions refuse hosts other than github.com, so
// workflows use these instead (same inputs and outputs). They are served
// from the action cache, whatever the ref, without the network.

//go:embed shims
var shims embed.FS

// shimFiles returns an embedded action's files ("upload-artifact"), nil if
// there is none.
func shimFiles(name string) map[string][]byte {
	entries, err := shims.ReadDir("shims/" + name)
	if err != nil {
		return nil
	}
	files := map[string][]byte{}
	for _, e := range entries {
		data, err := shims.ReadFile("shims/" + name + "/" + e.Name())
		if err != nil {
			return nil
		}
		files[e.Name()] = data
	}
	lib, err := shims.ReadFile("shims/lib.js")
	if err != nil {
		return nil
	}
	files["lib.js"] = lib
	return files
}

// shimFor names the embedded action a `uses:` URL points at
// (<server or github.com>/ogit/<name>).
func shimFor(url string) string {
	org, name := path.Base(path.Dir(url)), path.Base(url)
	if org != "ogit" || shimFiles(name) == nil {
		return ""
	}
	return name
}

var shimMu sync.Mutex

// writeShim stores an embedded action as a bare repository at gitPath and
// returns its commit (the same for every ref). Jobs running in parallel
// share it: it is rewritten only when its content changed.
func writeShim(gitPath, name string) (string, error) {
	shimMu.Lock()
	defer shimMu.Unlock()
	fs := memfs.New()
	repo, err := git.Init(memory.NewStorage(), fs)
	if err != nil {
		return "", err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", err
	}
	for file, data := range shimFiles(name) {
		f, err := fs.Create(file)
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			return "", err
		}
		f.Close()
		if _, err := wt.Add(file); err != nil {
			return "", err
		}
	}
	sig := &object.Signature{Name: "oGit", Email: "noreply@ogit", When: time.Unix(0, 0).UTC()}
	sha, err := wt.Commit("ogit/"+name, &git.CommitOptions{Author: sig, Committer: sig})
	if err != nil {
		return "", err
	}
	if existing, err := git.PlainOpen(gitPath); err == nil {
		if ref, err := existing.Reference("refs/tags/v4", false); err == nil && ref.Hash() == sha {
			return sha.String(), nil
		}
	}
	if err := os.RemoveAll(gitPath); err != nil {
		return "", err
	}
	// Copy it into a bare repository on disk, where act reads actions.
	bare, err := git.PlainInit(gitPath, true)
	if err != nil {
		return "", err
	}
	iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
	if err != nil {
		return "", err
	}
	if err := iter.ForEach(func(o plumbing.EncodedObject) error {
		_, err := bare.Storer.SetEncodedObject(o)
		return err
	}); err != nil {
		return "", err
	}
	for _, ref := range []string{"refs/heads/main", "refs/tags/v4", "refs/tags/v1"} {
		if err := bare.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(ref), sha)); err != nil {
			return "", err
		}
	}
	return sha.String(), nil
}

// safeFilename is act's name for an action's cache directory.
func safeFilename(s string) string {
	return strings.NewReplacer(`<`, "-", `>`, "-", `:`, "-", `"`, "-", `/`, "-", `\`, "-", `|`, "-", `?`, "-", `*`, "-").Replace(s)
}
