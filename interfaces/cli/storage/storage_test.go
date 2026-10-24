package storage

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/migration"
	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepackCommand(t *testing.T) {
	ep := s3test.StartFake(t)
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "ogit.sqlite")
	s3test.Configure(&cfg, ep)

	db, err := database.Configure(cfg, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, migration.RunMigration(zerolog.Nop(), db.Db))
	for _, r := range []domain.Repository{
		{Id: "11111111-1111-4111-8111-111111111111", Name: "Old", Slug: "old", StorageKey: "old"},
		{Id: "22222222-2222-4222-8222-222222222222", Name: "Also Old", Slug: "also-old", StorageKey: "legacy-key"},
	} {
		require.NoError(t, db.Db.Create(&r).Error)
		s3test.CreateLegacyRepository(t, ep, r.StorageKey, map[string]string{"README.md": r.Name})
	}

	var out bytes.Buffer
	assert.Error(t, repack(cfg, "", false, &out), "needs --repository or --all")
	assert.Error(t, repack(cfg, "old", true, &out), "not both")
	assert.Error(t, repack(cfg, "missing", false, &out))

	require.NoError(t, repack(cfg, "old", false, &out))
	assert.Equal(t, "old: packed 3 loose objects\n", out.String())

	out.Reset()
	require.NoError(t, repack(cfg, "", true, &out))
	assert.Contains(t, out.String(), "old: already packed\n")
	assert.Contains(t, out.String(), "also-old: packed 3 loose objects\n", "uses the storage key, not the slug")

	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = t.TempDir()
	out.Reset()
	require.NoError(t, repack(cfg, "", true, &out))
	assert.Contains(t, out.String(), "nothing to do")
}
