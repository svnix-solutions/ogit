package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRepositoryImport, downRepositoryImport)
}

// Imports from forges' APIs (issues, pull requests, labels, milestones,
// releases): one per repository, run once after its git data, resumable
// (stage and counts).
func upRepositoryImport(ctx context.Context, tx *sql.Tx) error {
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
		`CREATE TABLE repository_import (
			id ` + id + ` PRIMARY KEY,
			repository_id ` + id + ` NOT NULL UNIQUE REFERENCES repository(id) ON DELETE CASCADE,
			actor_id ` + id + ` NOT NULL,
			forge TEXT NOT NULL CHECK (forge IN ('github', 'gitlab', 'gitea')),
			api_url TEXT NOT NULL,
			project TEXT NOT NULL,
			secret TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK (status IN ('waiting', 'pending', 'running', 'done', 'failed')),
			stage TEXT NOT NULL CHECK (stage IN ('labels', 'milestones', 'issues', 'pulls', 'releases', 'done')),
			reserved INTEGER NOT NULL DEFAULT 0,
			pull_offset INTEGER NOT NULL DEFAULT 0,
			issues INTEGER NOT NULL DEFAULT 0,
			pulls INTEGER NOT NULL DEFAULT 0,
			releases INTEGER NOT NULL DEFAULT 0,
			comments INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			warnings TEXT NOT NULL DEFAULT '',
			failures INTEGER NOT NULL DEFAULT 0,
			next_run_at ` + ts + ` NOT NULL,
			locked_until ` + ts + `,
			finished_at ` + ts + `,
			created_at ` + ts + ` NOT NULL,
			updated_at ` + ts + ` NOT NULL
		);`,
		`CREATE INDEX idx_repository_import_due ON repository_import(status, next_run_at);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downRepositoryImport(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS repository_import;`)
	return err
}
