package actrunner

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestShims(t *testing.T) {
	for _, name := range []string{"upload-artifact", "download-artifact"} {
		files := shimFiles(name)
		require.NotNil(t, files, name)
		var action struct {
			Runs struct{ Using, Main string }
		}
		require.NoError(t, yaml.Unmarshal(files["action.yml"], &action), "%s/action.yml", name)
		assert.Equal(t, "node20", action.Runs.Using)
		assert.Contains(t, files, action.Runs.Main)
		assert.Contains(t, files, "lib.js")
	}
	assert.Equal(t, "upload-artifact", shimFor("https://git.example.com/ogit/upload-artifact"))
	assert.Equal(t, "download-artifact", shimFor("https://github.com/ogit/download-artifact"))
	assert.Empty(t, shimFor("https://github.com/actions/upload-artifact"))
	assert.Empty(t, shimFor("https://github.com/ogit/other"))

	dir := filepath.Join(t.TempDir(), "ogit-upload-artifact.git")
	sha, err := writeShim(dir, "upload-artifact")
	require.NoError(t, err)
	again, err := writeShim(dir, "upload-artifact")
	require.NoError(t, err)
	assert.Equal(t, sha, again, "the same commit every time")
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	for _, ref := range []string{"refs/tags/v4", "refs/tags/v1", "refs/heads/main"} {
		r, err := repo.Reference(plumbingRef(ref), false)
		require.NoError(t, err, ref)
		assert.Equal(t, sha, r.Hash().String())
	}
}

func plumbingRef(s string) plumbing.ReferenceName { return plumbing.ReferenceName(s) }
