package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upActions, downActions)
}

// Actions: runners, their registration tokens, workflow runs, their jobs
// and logs. Runs are numbered per repository by repository.last_run_number.
func upActions(ctx context.Context, tx *sql.Tx) error {
	var queries []string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		queries = []string{
			`ALTER TABLE repository ADD COLUMN last_run_number INTEGER NOT NULL DEFAULT 0;`,
			`CREATE TABLE action_runner (
				id TEXT PRIMARY KEY,
				name TEXT NOT NULL,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id TEXT,
				labels TEXT NOT NULL DEFAULT '',
				token_hash TEXT NOT NULL UNIQUE,
				version TEXT NOT NULL DEFAULT '',
				last_seen_at TIMESTAMP,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE INDEX idx_action_runner_scope ON action_runner(scope_type, scope_id);`,
			`CREATE TABLE action_registration_token (
				id TEXT PRIMARY KEY,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id TEXT,
				token_hash TEXT NOT NULL UNIQUE,
				created_by TEXT NOT NULL,
				expires_at TIMESTAMP NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`CREATE TABLE action_run (
				id TEXT PRIMARY KEY,
				repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
				number INTEGER NOT NULL,
				workflow_path TEXT NOT NULL,
				workflow_name TEXT NOT NULL DEFAULT '',
				workflow TEXT NOT NULL,
				event TEXT NOT NULL,
				event_payload TEXT NOT NULL DEFAULT '{}',
				ref TEXT NOT NULL DEFAULT '',
				sha TEXT NOT NULL,
				actor_id TEXT,
				actor_name TEXT NOT NULL DEFAULT '',
				labels TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'success', 'failure', 'cancelled')),
				runner_id TEXT,
				token_hash TEXT NOT NULL DEFAULT '',
				cancel_asked BOOLEAN NOT NULL DEFAULT FALSE,
				heartbeat_at TIMESTAMP,
				started_at TIMESTAMP,
				finished_at TIMESTAMP,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				UNIQUE(repository_id, number)
			);`,
			`CREATE INDEX idx_action_run_status ON action_run(status, created_at);`,
			`CREATE INDEX idx_action_run_token ON action_run(token_hash);`,
			`CREATE TABLE action_job (
				id TEXT PRIMARY KEY,
				run_id TEXT NOT NULL REFERENCES action_run(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				status TEXT NOT NULL,
				started_at TIMESTAMP,
				finished_at TIMESTAMP,
				UNIQUE(run_id, name)
			);`,
			`CREATE TABLE action_log (
				run_id TEXT NOT NULL REFERENCES action_run(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				data TEXT NOT NULL,
				PRIMARY KEY (run_id, seq)
			);`,
		}
	case "postgres":
		queries = []string{
			`ALTER TABLE repository ADD COLUMN last_run_number INTEGER NOT NULL DEFAULT 0;`,
			`CREATE TABLE action_runner (
				id UUID PRIMARY KEY,
				name TEXT NOT NULL,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id UUID,
				labels TEXT NOT NULL DEFAULT '',
				token_hash TEXT NOT NULL UNIQUE,
				version TEXT NOT NULL DEFAULT '',
				last_seen_at TIMESTAMPTZ,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE INDEX idx_action_runner_scope ON action_runner(scope_type, scope_id);`,
			`CREATE TABLE action_registration_token (
				id UUID PRIMARY KEY,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id UUID,
				token_hash TEXT NOT NULL UNIQUE,
				created_by UUID NOT NULL,
				expires_at TIMESTAMPTZ NOT NULL,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
			);`,
			`CREATE TABLE action_run (
				id UUID PRIMARY KEY,
				repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
				number INTEGER NOT NULL,
				workflow_path TEXT NOT NULL,
				workflow_name TEXT NOT NULL DEFAULT '',
				workflow TEXT NOT NULL,
				event TEXT NOT NULL,
				event_payload TEXT NOT NULL DEFAULT '{}',
				ref TEXT NOT NULL DEFAULT '',
				sha TEXT NOT NULL,
				actor_id UUID,
				actor_name TEXT NOT NULL DEFAULT '',
				labels TEXT NOT NULL DEFAULT '',
				status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'success', 'failure', 'cancelled')),
				runner_id UUID,
				token_hash TEXT NOT NULL DEFAULT '',
				cancel_asked BOOLEAN NOT NULL DEFAULT FALSE,
				heartbeat_at TIMESTAMPTZ,
				started_at TIMESTAMPTZ,
				finished_at TIMESTAMPTZ,
				created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
				UNIQUE(repository_id, number)
			);`,
			`CREATE INDEX idx_action_run_status ON action_run(status, created_at);`,
			`CREATE INDEX idx_action_run_token ON action_run(token_hash);`,
			`CREATE TABLE action_job (
				id UUID PRIMARY KEY,
				run_id UUID NOT NULL REFERENCES action_run(id) ON DELETE CASCADE,
				name TEXT NOT NULL,
				status TEXT NOT NULL,
				started_at TIMESTAMPTZ,
				finished_at TIMESTAMPTZ,
				UNIQUE(run_id, name)
			);`,
			`CREATE TABLE action_log (
				run_id UUID NOT NULL REFERENCES action_run(id) ON DELETE CASCADE,
				seq INTEGER NOT NULL,
				data TEXT NOT NULL,
				PRIMARY KEY (run_id, seq)
			);`,
		}
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downActions(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS action_log;`,
		`DROP TABLE IF EXISTS action_job;`,
		`DROP TABLE IF EXISTS action_run;`,
		`DROP TABLE IF EXISTS action_registration_token;`,
		`DROP TABLE IF EXISTS action_runner;`,
		`ALTER TABLE repository DROP COLUMN last_run_number;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
