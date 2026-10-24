package migration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Users that exist before the email verification migration must come out
// verified, so enabling verification never locks them out.
func TestEmailVerificationMigrationVerifiesExistingUsers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ogit.sqlite")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(migrationFiles)
	require.NoError(t, goose.SetDialect("sqlite3"))
	ctx := context.WithValue(context.Background(), "dbDialect", "sqlite")

	const before, verification = 20261024120000, 20261024130000
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", before))
	require.NoError(t, db.Exec(`INSERT INTO user (id, username, email, password) VALUES ('u1', 'old', 'old@example.com', 'x')`).Error)

	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", verification))
	var verifiedAt *string
	require.NoError(t, db.Raw(`SELECT email_verified_at FROM user WHERE id = 'u1'`).Scan(&verifiedAt).Error)
	assert.NotNil(t, verifiedAt, "existing user is marked verified")

	// The migration can be rolled back and re-applied.
	require.NoError(t, goose.DownToContext(ctx, sqlDB, "files", before))
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", verification))
}

func TestSSHKeyReworkMigrationRollsBack(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ogit.sqlite")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(migrationFiles)
	require.NoError(t, goose.SetDialect("sqlite3"))
	ctx := context.WithValue(context.Background(), "dbDialect", "sqlite")

	const before = 20261024130000
	require.NoError(t, goose.UpContext(ctx, sqlDB, "files"))
	assert.True(t, db.Migrator().HasColumn("ssh_key", "fingerprint"))
	assert.False(t, db.Migrator().HasColumn("ssh_key", "private_key"), "private keys are never stored")

	require.NoError(t, goose.DownToContext(ctx, sqlDB, "files", before))
	assert.True(t, db.Migrator().HasColumn("ssh_key", "private_key"), "rollback restores the original table")
	require.NoError(t, goose.UpContext(ctx, sqlDB, "files"))
}

// Repositories that exist before renames were introduced keep their slug as
// storage key, so their git data is found without moving it.
func TestRepositoryLifecycleMigrationBackfillsStorageKey(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ogit.sqlite")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(migrationFiles)
	require.NoError(t, goose.SetDialect("sqlite3"))
	ctx := context.WithValue(context.Background(), "dbDialect", "sqlite")

	const before = 20261024140000
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", before))
	require.NoError(t, db.Exec(`INSERT INTO repository (id, name, slug, created_at, updated_at) VALUES ('r1', 'Legacy', 'legacy', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)

	require.NoError(t, goose.UpContext(ctx, sqlDB, "files"))
	var key string
	require.NoError(t, db.Raw(`SELECT storage_key FROM repository WHERE id = 'r1'`).Scan(&key).Error)
	assert.Equal(t, "legacy", key)

	require.NoError(t, goose.DownToContext(ctx, sqlDB, "files", before))
	require.NoError(t, goose.UpContext(ctx, sqlDB, "files"))
}
