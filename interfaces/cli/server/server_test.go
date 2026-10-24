package server

import (
	"path/filepath"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetupDepsRejectsShortSessionSecret(t *testing.T) {
	// Otherwise valid config, so the secret is the only reason to fail.
	dir := t.TempDir()
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(dir, "ogit.sqlite")
	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = filepath.Join(dir, "repositories")
	cfg.Session.SecretKey = "supersecretkey"
	_, err := setupDeps(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session.secret_key")
}

// Duplicate names or aliases make urfave/cli silently bind the same switch
// to more than one flag (e.g. -l was both --http.logs and --logger.level).
func TestFlagNamesAreUnique(t *testing.T) {
	var cfg config.Config
	seen := map[string]string{}
	for _, f := range getFlags(&cfg) {
		owner := f.Names()[0]
		for _, name := range f.Names() {
			if prev, ok := seen[name]; ok {
				t.Errorf("flag name %q is used by both %s and %s", name, prev, owner)
			}
			seen[name] = owner
		}
	}
}
