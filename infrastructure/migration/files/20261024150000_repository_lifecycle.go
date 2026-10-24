package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRepositoryLifecycle, downRepositoryLifecycle)
}

// Repository renames:
//   - repository.storage_key is where the git data lives. Existing
//     repositories keep their slug (no data moves); new ones use their id,
//     so a rename never touches storage.
//   - repository_redirect maps old slugs to the renamed repository.
func upRepositoryLifecycle(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE repository ADD COLUMN storage_key TEXT;
		UPDATE repository SET storage_key = slug;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_repository_storage_key ON repository(storage_key);

		CREATE TABLE IF NOT EXISTS repository_redirect (
			slug TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (repository_id) REFERENCES repository(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	case "postgres":
		query = `
		ALTER TABLE repository ADD COLUMN IF NOT EXISTS storage_key TEXT;
		UPDATE repository SET storage_key = slug WHERE storage_key IS NULL;
		ALTER TABLE repository ALTER COLUMN storage_key SET NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS idx_repository_storage_key ON repository(storage_key);

		CREATE TABLE IF NOT EXISTS repository_redirect (
			slug TEXT PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX IF NOT EXISTS idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}

// downRepositoryLifecycle is only safe while every repository's storage_key
// still equals its slug: repositories created or renamed after the upgrade
// would no longer be found in storage.
func downRepositoryLifecycle(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)

	var query string
	switch dialect {
	case "sqlite":
		query = `
		DROP TABLE IF EXISTS repository_redirect;
		DROP INDEX IF EXISTS idx_repository_storage_key;
		ALTER TABLE repository DROP COLUMN storage_key;
		`
	case "postgres":
		query = `
		DROP TABLE IF EXISTS repository_redirect;
		DROP INDEX IF EXISTS idx_repository_storage_key;
		ALTER TABLE repository DROP COLUMN IF EXISTS storage_key;
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}
