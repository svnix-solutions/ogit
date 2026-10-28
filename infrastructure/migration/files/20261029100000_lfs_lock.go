package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upLFSLock, downLFSLock)
}

// Git LFS file locks: one per path and repository.
func upLFSLock(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE lfs_lock (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			path TEXT NOT NULL,
			owner_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			ref TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, path)
		);
		`
	case "postgres":
		query = `
		CREATE TABLE lfs_lock (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			path TEXT NOT NULL,
			owner_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			ref TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, path)
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downLFSLock(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS lfs_lock;`)
	return err
}
