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
