package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upCommitStatus, downCommitStatus)
}

// Commit statuses, and the contexts a branch protection rule requires.
func upCommitStatus(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE commit_status (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			sha TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'success', 'failure', 'error')),
			context TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			target_url TEXT NOT NULL DEFAULT '',
			creator_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_commit_status_sha ON commit_status(repository_id, sha, context, created_at);
		CREATE INDEX idx_commit_status_recent ON commit_status(repository_id, created_at);
		ALTER TABLE branch_protection ADD COLUMN required_contexts TEXT NOT NULL DEFAULT '[]';
		`
	case "postgres":
		query = `
		CREATE TABLE commit_status (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			sha TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('pending', 'success', 'failure', 'error')),
			context TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			target_url TEXT NOT NULL DEFAULT '',
			creator_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_commit_status_sha ON commit_status(repository_id, sha, context, created_at);
		CREATE INDEX idx_commit_status_recent ON commit_status(repository_id, created_at);
		ALTER TABLE branch_protection ADD COLUMN required_contexts TEXT NOT NULL DEFAULT '[]';
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downCommitStatus(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS commit_status;`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `ALTER TABLE branch_protection DROP COLUMN required_contexts;`)
	return err
}
