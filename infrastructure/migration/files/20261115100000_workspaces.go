package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWorkspaces, downWorkspaces)
}

// Workspaces: per-user development containers, one per user and branch.
func upWorkspaces(ctx context.Context, tx *sql.Tx) error {
	idType, timeType, userTable := "TEXT", "TIMESTAMP", "user"
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
	case "postgres":
		idType, timeType, userTable = "UUID", "TIMESTAMPTZ", `"user"`
	default:
		return fmt.Errorf("unsupported dialect %q", dialect)
	}
	query := fmt.Sprintf(`
	CREATE TABLE workspace (
		id %[1]s PRIMARY KEY,
		repository_id %[1]s NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
		user_id %[1]s NOT NULL REFERENCES %[3]s(id) ON DELETE CASCADE,
		branch TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('stopped', 'starting', 'running', 'stopping')),
		last_used_at %[2]s NOT NULL,
		created_at %[2]s NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at %[2]s NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE (user_id, repository_id, branch)
	);
	CREATE INDEX idx_workspace_repository ON workspace(repository_id);`, idType, timeType, userTable)
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downWorkspaces(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE workspace;`)
	return err
}
