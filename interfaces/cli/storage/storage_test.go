package storage

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

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
	owner := createOwner(t, db)
	for _, r := range []domain.Repository{
		{Id: "11111111-1111-4111-8111-111111111111", Name: "Old", Slug: "old", StorageKey: "old"},
		{Id: "22222222-2222-4222-8222-222222222222", Name: "Also Old", Slug: "also-old", StorageKey: "legacy-key"},
	} {
		ownedBy(&r, owner)
		require.NoError(t, db.Db.Create(&r).Error)
		s3test.CreateLegacyRepository(t, ep, r.StorageKey, map[string]string{"README.md": r.Name})
	}

	var out bytes.Buffer
	assert.Error(t, repack(cfg, "", false, &out), "needs --repository or --all")
	assert.Error(t, repack(cfg, "alice/old", true, &out), "not both")
	assert.Error(t, repack(cfg, "alice/missing", false, &out))
	assert.Error(t, repack(cfg, "old", false, &out), "needs <owner>/<name>")

	require.NoError(t, repack(cfg, "alice/old", false, &out))
	assert.Equal(t, "alice/old: packed 3 loose objects\n", out.String())

	out.Reset()
	require.NoError(t, repack(cfg, "", true, &out))
	assert.Contains(t, out.String(), "alice/old: already packed\n")
	assert.Contains(t, out.String(), "alice/also-old: packed 3 loose objects\n", "uses the storage key, not the slug")

	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = t.TempDir()
	out.Reset()
	require.NoError(t, repack(cfg, "", true, &out))
	assert.Contains(t, out.String(), "nothing to do")
}

func TestGCCommand(t *testing.T) {
	ep := s3test.StartFake(t)
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "ogit.sqlite")
	s3test.Configure(&cfg, ep)

	db, err := database.Configure(cfg, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, migration.RunMigration(zerolog.Nop(), db.Db))
	repo := domain.Repository{Id: "33333333-3333-4333-8333-333333333333", Name: "Busy", Slug: "busy", StorageKey: "busy-key"}
	ownedBy(&repo, createOwner(t, db))
	require.NoError(t, db.Db.Create(&repo).Error)
	// One pack (the initial commit) plus loose objects from an old server.
	require.NoError(t, s3test.NewStorage(t, ep).CreateRepository(repo.StorageKey, "Test"))
	s3test.CreateLegacyRepository(t, ep, repo.StorageKey, map[string]string{"README.md": "old"})

	var out bytes.Buffer
	assert.Error(t, gc(cfg, "", false, time.Hour, &out), "needs --repository or --all")
	assert.Error(t, gc(cfg, "alice/busy", false, -time.Second, &out))

	require.NoError(t, gc(cfg, "alice/busy", false, time.Hour, &out))
	assert.Equal(t, "alice/busy: packed 3 loose objects, merged 2 packs (6 objects)\n", out.String(), "uses the storage key")

	out.Reset()
	require.NoError(t, gc(cfg, "", true, 0, &out))
	assert.Equal(t, "alice/busy: deleted 2 replaced packs\n", out.String())

	out.Reset()
	require.NoError(t, gc(cfg, "", true, 0, &out))
	assert.Equal(t, "alice/busy: nothing to do\n", out.String())

	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = t.TempDir()
	out.Reset()
	require.NoError(t, gc(cfg, "", true, 0, &out))
	assert.Contains(t, out.String(), "nothing to do")
}

func TestPruneCommand(t *testing.T) {
	ep := s3test.StartFake(t)
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "ogit.sqlite")
	s3test.Configure(&cfg, ep)

	db, err := database.Configure(cfg, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, migration.RunMigration(zerolog.Nop(), db.Db))
	repo := domain.Repository{Id: "44444444-4444-4444-8444-444444444444", Name: "Forced", Slug: "forced", StorageKey: "forced-key"}
	ownedBy(&repo, createOwner(t, db))
	require.NoError(t, db.Db.Create(&repo).Error)
	// The initial commit, then an old-layout history replacing it: the
	// initial commit (a blob, a tree and the commit) is unreachable.
	require.NoError(t, s3test.NewStorage(t, ep).CreateRepository(repo.StorageKey, "Test"))
	s3test.CreateLegacyRepository(t, ep, repo.StorageKey, map[string]string{"README.md": "rewritten"})
	time.Sleep(20 * time.Millisecond)

	var out bytes.Buffer
	assert.Error(t, prune(cfg, "", false, time.Hour, time.Hour, false, &out), "needs --repository or --all")
	assert.Error(t, prune(cfg, "alice/forced", false, -time.Hour, time.Hour, false, &out))

	require.NoError(t, prune(cfg, "alice/forced", false, 0, time.Hour, true, &out))
	assert.Equal(t, "alice/forced: would prune 3 unreachable objects (from 1 old packs, 0 objects kept)\n", out.String(), "dry run; loose objects are left alone")

	out.Reset()
	require.NoError(t, prune(cfg, "alice/forced", false, 14*24*time.Hour, time.Hour, false, &out))
	assert.Equal(t, "alice/forced: packed 3 loose objects\n", out.String(), "nothing old enough to prune")

	time.Sleep(20 * time.Millisecond)
	out.Reset()
	require.NoError(t, prune(cfg, "", true, 0, 0, false, &out))
	assert.Equal(t, "alice/forced: pruned 3 unreachable objects (from 2 old packs, 3 objects kept)\n", out.String())

	out.Reset()
	require.NoError(t, prune(cfg, "", true, 0, 0, false, &out))
	assert.Equal(t, "alice/forced: deleted 1 replaced packs\n", out.String(), "the kept objects re-encode to the same pack as before: only the other one went")

	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = t.TempDir()
	out.Reset()
	require.NoError(t, prune(cfg, "", true, 0, 0, false, &out))
	assert.Contains(t, out.String(), "nothing to do")
}

// createOwner inserts the user "alice", whose namespace the test repositories
// live in.
func createOwner(t *testing.T, db database.Config) domain.User {
	t.Helper()
	u := domain.User{Id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Username: "alice", Email: "alice@example.com", Active: true}
	require.NoError(t, db.Db.Create(&u).Error)
	return u
}

// ownedBy puts r in the user's namespace.
func ownedBy(r *domain.Repository, u domain.User) {
	r.Namespace, r.OwnerType, r.OwnerId = u.Username, domain.OwnerUser, u.Id
}
