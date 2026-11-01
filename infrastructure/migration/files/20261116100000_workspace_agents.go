package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWorkspaceAgents, downWorkspaceAgents)
}

// Workspace agents run workspaces in Docker. A workspace keeps its agent
// while stopped (the agent holds its volume); workspace_cleanup lists the
// containers and volumes agents still have to remove.
func upWorkspaceAgents(ctx context.Context, tx *sql.Tx) error {
	idType, timeType := "TEXT", "TIMESTAMP"
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
	case "postgres":
		idType, timeType = "UUID", "TIMESTAMPTZ"
	default:
		return fmt.Errorf("unsupported dialect %q", dialect)
	}
	query := fmt.Sprintf(`
	CREATE TABLE workspace_agent_token (
		id %[1]s PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		reusable BOOLEAN NOT NULL DEFAULT FALSE,
		token_hash TEXT NOT NULL UNIQUE,
		created_by %[1]s NOT NULL,
		expires_at %[2]s,
		last_used_at %[2]s,
		created_at %[2]s NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE workspace_agent (
		id %[1]s PRIMARY KEY,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		version TEXT NOT NULL DEFAULT '',
		registration_token_id %[1]s,
		last_seen_at %[2]s NOT NULL,
		created_at %[2]s NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE TABLE workspace_cleanup (
		workspace_id %[1]s PRIMARY KEY,
		agent_id %[1]s NOT NULL,
		created_at %[2]s NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX idx_workspace_cleanup_agent ON workspace_cleanup(agent_id);
	ALTER TABLE workspace ADD COLUMN agent_id %[1]s;
	ALTER TABLE workspace ADD COLUMN token_hash TEXT;
	ALTER TABLE workspace ADD COLUMN token_expires_at %[2]s;
	ALTER TABLE workspace ADD COLUMN failure TEXT NOT NULL DEFAULT '';
	CREATE INDEX idx_workspace_agent ON workspace(agent_id);
	CREATE UNIQUE INDEX idx_workspace_token ON workspace(token_hash);`, idType, timeType)
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downWorkspaceAgents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
	DROP INDEX idx_workspace_token;
	DROP INDEX idx_workspace_agent;
	ALTER TABLE workspace DROP COLUMN failure;
	ALTER TABLE workspace DROP COLUMN token_expires_at;
	ALTER TABLE workspace DROP COLUMN token_hash;
	ALTER TABLE workspace DROP COLUMN agent_id;
	DROP TABLE workspace_cleanup;
	DROP TABLE workspace_agent;
	DROP TABLE workspace_agent_token;`)
	return err
}
