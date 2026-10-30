package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRunnerScaling, downRunnerScaling)
}

// Runner scaling: reusable registration tokens (which may never expire),
// and runners that are ephemeral or registered with a reusable token.
func upRunnerScaling(ctx context.Context, tx *sql.Tx) error {
	var queries []string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		// SQLite can't drop NOT NULL on expires_at: rebuild the table (no
		// foreign keys point at it).
		queries = []string{
			`CREATE TABLE action_registration_token_new (
				id TEXT PRIMARY KEY,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id TEXT,
				name TEXT NOT NULL DEFAULT '',
				reusable BOOLEAN NOT NULL DEFAULT FALSE,
				token_hash TEXT NOT NULL UNIQUE,
				created_by TEXT NOT NULL,
				expires_at TIMESTAMP,
				last_used_at TIMESTAMP,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`INSERT INTO action_registration_token_new (id, scope_type, scope_id, token_hash, created_by, expires_at, created_at)
				SELECT id, scope_type, scope_id, token_hash, created_by, expires_at, created_at FROM action_registration_token;`,
			`DROP TABLE action_registration_token;`,
			`ALTER TABLE action_registration_token_new RENAME TO action_registration_token;`,
			`ALTER TABLE action_runner ADD COLUMN ephemeral BOOLEAN NOT NULL DEFAULT FALSE;`,
			`ALTER TABLE action_runner ADD COLUMN spent BOOLEAN NOT NULL DEFAULT FALSE;`,
			`ALTER TABLE action_runner ADD COLUMN registration_token_id TEXT;`,
		}
	case "postgres":
		queries = []string{
			`ALTER TABLE action_registration_token ALTER COLUMN expires_at DROP NOT NULL;`,
			`ALTER TABLE action_registration_token ADD COLUMN name TEXT NOT NULL DEFAULT '';`,
			`ALTER TABLE action_registration_token ADD COLUMN reusable BOOLEAN NOT NULL DEFAULT FALSE;`,
			`ALTER TABLE action_registration_token ADD COLUMN last_used_at TIMESTAMPTZ;`,
			`ALTER TABLE action_runner ADD COLUMN ephemeral BOOLEAN NOT NULL DEFAULT FALSE;`,
			`ALTER TABLE action_runner ADD COLUMN spent BOOLEAN NOT NULL DEFAULT FALSE;`,
			// No foreign key: revoking a token keeps its runners.
			`ALTER TABLE action_runner ADD COLUMN registration_token_id UUID;`,
		}
	}
	queries = append(queries, `CREATE INDEX idx_action_runner_registration_token ON action_runner(registration_token_id);`)
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downRunnerScaling(ctx context.Context, tx *sql.Tx) error {
	var queries []string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		queries = []string{
			`CREATE TABLE action_registration_token_old (
				id TEXT PRIMARY KEY,
				scope_type TEXT NOT NULL CHECK (scope_type IN ('site', 'group', 'repository')),
				scope_id TEXT,
				token_hash TEXT NOT NULL UNIQUE,
				created_by TEXT NOT NULL,
				expires_at TIMESTAMP NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`INSERT INTO action_registration_token_old (id, scope_type, scope_id, token_hash, created_by, expires_at, created_at)
				SELECT id, scope_type, scope_id, token_hash, created_by, expires_at, created_at FROM action_registration_token WHERE NOT reusable;`,
			`DROP TABLE action_registration_token;`,
			`ALTER TABLE action_registration_token_old RENAME TO action_registration_token;`,
		}
	case "postgres":
		queries = []string{
			`DELETE FROM action_registration_token WHERE reusable;`,
			`ALTER TABLE action_registration_token ALTER COLUMN expires_at SET NOT NULL;`,
			`ALTER TABLE action_registration_token DROP COLUMN name;`,
			`ALTER TABLE action_registration_token DROP COLUMN reusable;`,
			`ALTER TABLE action_registration_token DROP COLUMN last_used_at;`,
		}
	}
	queries = append(queries,
		`DROP INDEX idx_action_runner_registration_token;`,
		`ALTER TABLE action_runner DROP COLUMN registration_token_id;`,
		`ALTER TABLE action_runner DROP COLUMN spent;`,
		`ALTER TABLE action_runner DROP COLUMN ephemeral;`,
	)
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
