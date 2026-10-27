package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRepositoryPushMirror, downRepositoryPushMirror)
}

// Push mirrors: a repository's branches and tags pushed to remotes after
// each push and every interval (credentials like pull mirrors').
func upRepositoryPushMirror(ctx context.Context, tx *sql.Tx) error {
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
		`CREATE TABLE repository_push_mirror (
			id ` + id + ` PRIMARY KEY,
			repository_id ` + id + ` NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			auth TEXT NOT NULL CHECK (auth IN ('none', 'password', 'ssh')),
			username TEXT NOT NULL DEFAULT '',
			secret TEXT NOT NULL DEFAULT '',
			ssh_public_key TEXT NOT NULL DEFAULT '',
			ssh_host_key TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL CHECK (mode IN ('mirror', 'branches')),
			lfs BOOLEAN NOT NULL DEFAULT FALSE,
			interval_seconds INTEGER NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending', 'syncing', 'ok', 'failed')),
			last_error TEXT NOT NULL DEFAULT '',
			failures INTEGER NOT NULL DEFAULT 0,
			generation INTEGER NOT NULL DEFAULT 0,
			last_sync_at ` + ts + `,
			last_success_at ` + ts + `,
			next_sync_at ` + ts + ` NOT NULL,
			locked_until ` + ts + `,
			created_at ` + ts + ` NOT NULL,
			updated_at ` + ts + ` NOT NULL
		);`,
		`CREATE INDEX idx_repository_push_mirror_repository ON repository_push_mirror(repository_id);`,
		`CREATE INDEX idx_repository_push_mirror_due ON repository_push_mirror(next_sync_at);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downRepositoryPushMirror(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS repository_push_mirror;`)
	return err
}
