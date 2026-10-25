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

	const before, lifecycle = 20261024140000, 20261024150000
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", before))
	require.NoError(t, db.Exec(`INSERT INTO repository (id, name, slug, created_at, updated_at) VALUES ('r1', 'Legacy', 'legacy', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)

	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", lifecycle))
	var key string
	require.NoError(t, db.Raw(`SELECT storage_key FROM repository WHERE id = 'r1'`).Scan(&key).Error)
	assert.Equal(t, "legacy", key)

	require.NoError(t, goose.DownToContext(ctx, sqlDB, "files", before))
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", lifecycle))
}

func migratedTo(t *testing.T, version int64) (*gorm.DB, func(int64), func(int64)) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ogit.sqlite")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	goose.SetBaseFS(migrationFiles)
	require.NoError(t, goose.SetDialect("sqlite3"))
	ctx := context.WithValue(context.Background(), "dbDialect", "sqlite")
	require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", version))
	up := func(v int64) { t.Helper(); require.NoError(t, goose.UpToContext(ctx, sqlDB, "files", v)) }
	down := func(v int64) { t.Helper(); require.NoError(t, goose.DownToContext(ctx, sqlDB, "files", v)) }
	return db, up, down
}

const beforeNamespaces, namespaces = 20261024150000, 20261025100000

// Existing repositories move under their oldest user owner, keep their
// data (storage key) and lose their flat redirects.
func TestNamespacesMigration(t *testing.T) {
	db, up, down := migratedTo(t, beforeNamespaces)
	for _, q := range []string{
		`INSERT INTO user (id, username, email, password) VALUES ('u1', 'alice', 'a@example.com', 'x'), ('u2', 'bob', 'b@example.com', 'x')`,
		`INSERT INTO repository (id, name, slug, storage_key, is_private, created_at, updated_at) VALUES
			('r1', 'Website', 'website', 'website', false, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP),
			('r2', 'Infra', 'infra', 'r2', true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		// r1: bob became an owner after alice; r2: bob only.
		`INSERT INTO repository_member (id, repository_id, member_type, user_id, access_type, created_at, updated_at) VALUES
			('m1', 'r1', 'user', 'u1', 'owner', '2026-01-01', '2026-01-01'),
			('m2', 'r1', 'user', 'u2', 'owner', '2026-02-01', '2026-02-01'),
			('m3', 'r2', 'user', 'u1', 'developer', '2026-01-01', '2026-01-01'),
			('m4', 'r2', 'user', 'u2', 'owner', '2026-03-01', '2026-03-01')`,
		`INSERT INTO repository_redirect (slug, repository_id) VALUES ('old-website', 'r1')`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}

	up(namespaces)
	type row struct{ Id, Namespace, OwnerType, OwnerId, Slug, StorageKey string }
	var rows []row
	require.NoError(t, db.Raw(`SELECT id, namespace, owner_type, owner_id, slug, storage_key FROM repository ORDER BY id`).Scan(&rows).Error)
	assert.Equal(t, []row{
		{"r1", "alice", "user", "u1", "website", "website"},
		{"r2", "bob", "user", "u2", "infra", "r2"},
	}, rows)
	var redirects int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM repository_redirect`).Scan(&redirects).Error)
	assert.Zero(t, redirects, "flat redirects are dropped")

	// Slugs are unique per namespace now.
	insert := func(id, ns string) error {
		return db.Exec(`INSERT INTO repository (id, name, namespace, owner_type, owner_id, slug, storage_key) VALUES (?, 'Website', ?, 'user', 'u2', 'website', ?)`, id, ns, id).Error
	}
	require.NoError(t, insert("r3", "bob"), "same slug and name in another namespace")
	assert.Error(t, insert("r4", "bob"), "but not twice in one")
	require.NoError(t, db.Exec(`INSERT INTO repository_redirect (namespace, slug, repository_id) VALUES ('alice', 'old', 'r1')`).Error)

	// Rolling back works while slugs are still globally unique.
	require.NoError(t, db.Exec(`DELETE FROM repository WHERE id = 'r3'`).Error)
	down(beforeNamespaces)
	var slug string
	require.NoError(t, db.Raw(`SELECT slug FROM repository WHERE id = 'r1'`).Scan(&slug).Error)
	assert.Equal(t, "website", slug)
	up(namespaces)
}

func TestNamespacesMigrationNamesOrphans(t *testing.T) {
	db, _, _ := migratedTo(t, beforeNamespaces)
	require.NoError(t, db.Exec(`INSERT INTO repository (id, name, slug, storage_key, created_at, updated_at) VALUES ('r1', 'Lost', 'lost', 'lost', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), "dbDialect", "sqlite")
	err = goose.UpToContext(ctx, sqlDB, "files", namespaces)
	require.ErrorContains(t, err, "without a user owner can't be given a namespace: lost")
}

// Issues share pull requests' numbers: the counter starts after the
// highest existing pull request, and issues are on for every repository.
func TestIssueMigrationContinuesPullRequestNumbers(t *testing.T) {
	const beforeIssues, issues = 20261025150000, 20261025160000
	db, up, down := migratedTo(t, beforeIssues)
	for _, q := range []string{
		`INSERT INTO user (id, username, email, password) VALUES ('u1', 'alice', 'a@example.com', 'x')`,
		`INSERT INTO repository (id, name, namespace, owner_type, owner_id, slug, storage_key) VALUES
			('r1', 'One', 'alice', 'user', 'u1', 'one', 'r1'), ('r2', 'Two', 'alice', 'user', 'u1', 'two', 'r2')`,
		`INSERT INTO pull_request (id, repository_id, number, title, source_branch, target_branch, state) VALUES
			('p1', 'r1', 1, 'a', 'x', 'main', 'open'), ('p2', 'r1', 4, 'b', 'y', 'main', 'merged')`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}

	up(issues)
	type row struct {
		Id         string
		LastNumber int
		HasIssues  bool
	}
	var rows []row
	require.NoError(t, db.Raw(`SELECT id, last_number, has_issues FROM repository ORDER BY id`).Scan(&rows).Error)
	assert.Equal(t, []row{{"r1", 4, true}, {"r2", 0, true}}, rows)

	down(beforeIssues)
	up(issues)
}
