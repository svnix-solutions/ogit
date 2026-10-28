package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upBranchProtection, downBranchProtection)
}

// Branch protection rules, one per pattern and repository.
func upBranchProtection(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE branch_protection (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			pattern TEXT NOT NULL,
			allow_force_push BOOLEAN NOT NULL DEFAULT FALSE,
			allow_deletion BOOLEAN NOT NULL DEFAULT FALSE,
			require_pull_request BOOLEAN NOT NULL DEFAULT FALSE,
			required_approvals INTEGER,
			dismiss_stale_approvals BOOLEAN NOT NULL DEFAULT FALSE,
			allowed_user_ids TEXT NOT NULL DEFAULT '[]',
			allowed_group_ids TEXT NOT NULL DEFAULT '[]',
			owners_bypass BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, pattern)
		);
		`
	case "postgres":
		query = `
		CREATE TABLE branch_protection (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			pattern TEXT NOT NULL,
			allow_force_push BOOLEAN NOT NULL DEFAULT FALSE,
			allow_deletion BOOLEAN NOT NULL DEFAULT FALSE,
			require_pull_request BOOLEAN NOT NULL DEFAULT FALSE,
			required_approvals INTEGER,
			dismiss_stale_approvals BOOLEAN NOT NULL DEFAULT FALSE,
			allowed_user_ids TEXT NOT NULL DEFAULT '[]',
			allowed_group_ids TEXT NOT NULL DEFAULT '[]',
			owners_bypass BOOLEAN NOT NULL DEFAULT FALSE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, pattern)
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downBranchProtection(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS branch_protection;`)
	return err
}
