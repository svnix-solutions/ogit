package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRepositoryMirror, downRepositoryMirror)
}

// Pull mirrors and imports: one per repository, fetching a remote (HTTPS
// with a password or token, or SSH with a generated key; secrets sealed
// with security.encryption_key). A mirror keeps syncing and its
// repository refuses pushes (repository.is_mirror); an import syncs once
// and its row goes.
func upRepositoryMirror(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	ts, id := "TIMESTAMP", "TEXT"
	switch dialect {
	case "sqlite":
	case "postgres":
		ts, id = "TIMESTAMPTZ", "UUID"
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	for _, q := range []string{
		`ALTER TABLE repository ADD COLUMN is_mirror BOOLEAN NOT NULL DEFAULT FALSE;`,
		`CREATE TABLE repository_mirror (
			id ` + id + ` PRIMARY KEY,
			repository_id ` + id + ` NOT NULL UNIQUE REFERENCES repository(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			mirror BOOLEAN NOT NULL,
			auth TEXT NOT NULL CHECK (auth IN ('none', 'password', 'ssh')),
			username TEXT NOT NULL DEFAULT '',
			secret TEXT NOT NULL DEFAULT '',
			ssh_public_key TEXT NOT NULL DEFAULT '',
			ssh_host_key TEXT NOT NULL DEFAULT '',
			lfs BOOLEAN NOT NULL DEFAULT FALSE,
			interval_seconds INTEGER NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'syncing', 'ok', 'failed')),
			last_error TEXT NOT NULL DEFAULT '',
			failures INTEGER NOT NULL DEFAULT 0,
			last_sync_at ` + ts + `,
			last_success_at ` + ts + `,
			next_sync_at ` + ts + ` NOT NULL,
			locked_until ` + ts + `,
			created_at ` + ts + ` NOT NULL,
			updated_at ` + ts + ` NOT NULL
		);`,
		`CREATE INDEX idx_repository_mirror_due ON repository_mirror(next_sync_at);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downRepositoryMirror(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{`DROP TABLE IF EXISTS repository_mirror;`, `ALTER TABLE repository DROP COLUMN is_mirror;`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
