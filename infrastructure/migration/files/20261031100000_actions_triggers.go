package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upActionsTriggers, downActionsTriggers)
}

// Actions, part 2: secrets and variables per scope (the site's have an
// empty scope_id),
// workflow schedules, and what a run is allowed (fork, approval,
// permissions, the commit its statuses go on).
func upActionsTriggers(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	ts, now := "TIMESTAMP", "CURRENT_TIMESTAMP"
	switch dialect {
	case "sqlite":
	case "postgres":
		ts, now = "TIMESTAMPTZ", "NOW()"
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	queries := []string{
		`CREATE TABLE action_secret (
			id TEXT PRIMARY KEY,
			scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
			scope_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			created_at ` + ts + ` NOT NULL DEFAULT ` + now + `,
			updated_at ` + ts + ` NOT NULL DEFAULT ` + now + `,
			UNIQUE(scope_type, scope_id, name)
		);`,
		`CREATE TABLE action_variable (
			id TEXT PRIMARY KEY,
			scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
			scope_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			value TEXT NOT NULL,
			created_at ` + ts + ` NOT NULL DEFAULT ` + now + `,
			updated_at ` + ts + ` NOT NULL DEFAULT ` + now + `,
			UNIQUE(scope_type, scope_id, name)
		);`,
		`CREATE TABLE action_schedule (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			workflow_path TEXT NOT NULL,
			cron TEXT NOT NULL,
			next_run_at ` + ts + ` NOT NULL
		);`,
		`CREATE INDEX idx_action_schedule_due ON action_schedule(next_run_at);`,
		`CREATE INDEX idx_action_schedule_repo ON action_schedule(repository_id);`,
		`ALTER TABLE action_run ADD COLUMN needs_approval BOOLEAN NOT NULL DEFAULT FALSE;`,
		`ALTER TABLE action_run ADD COLUMN from_fork BOOLEAN NOT NULL DEFAULT FALSE;`,
		`ALTER TABLE action_run ADD COLUMN permissions TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE action_run ADD COLUMN status_sha TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE action_run ADD COLUMN pull_number INTEGER NOT NULL DEFAULT 0;`,
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downActionsTriggers(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS action_secret;`,
		`DROP TABLE IF EXISTS action_variable;`,
		`DROP TABLE IF EXISTS action_schedule;`,
		`ALTER TABLE action_run DROP COLUMN needs_approval;`,
		`ALTER TABLE action_run DROP COLUMN from_fork;`,
		`ALTER TABLE action_run DROP COLUMN permissions;`,
		`ALTER TABLE action_run DROP COLUMN status_sha;`,
		`ALTER TABLE action_run DROP COLUMN pull_number;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
