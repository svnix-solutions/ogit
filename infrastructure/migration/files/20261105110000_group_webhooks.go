package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upGroupWebhooks, downGroupWebhooks)
}

// Group webhooks: a hook belongs to a repository or to a group (firing
// for every repository below it). SQLite can't drop NOT NULL, so its
// table is rebuilt (deliveries refer to it by name).
func upGroupWebhooks(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	var queries []string
	switch dialect {
	case "sqlite":
		queries = []string{
			`CREATE TABLE webhook_new (
				id TEXT PRIMARY KEY,
				repository_id TEXT REFERENCES repository(id) ON DELETE CASCADE,
				group_id TEXT REFERENCES "group"(id) ON DELETE CASCADE,
				url TEXT NOT NULL,
				secret TEXT NOT NULL DEFAULT '',
				events TEXT NOT NULL DEFAULT '["push"]',
				active BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				CHECK ((repository_id IS NULL) <> (group_id IS NULL))
			);`,
			`INSERT INTO webhook_new (id, repository_id, url, secret, events, active, created_at, updated_at)
				SELECT id, repository_id, url, secret, events, active, created_at, updated_at FROM webhook;`,
			`DROP TABLE webhook;`,
			`ALTER TABLE webhook_new RENAME TO webhook;`,
			`CREATE INDEX idx_webhook_repository ON webhook(repository_id);`,
		}
	case "postgres":
		queries = []string{
			`ALTER TABLE webhook ALTER COLUMN repository_id DROP NOT NULL;`,
			`ALTER TABLE webhook ADD COLUMN group_id UUID REFERENCES "group"(id) ON DELETE CASCADE;`,
			`ALTER TABLE webhook ADD CONSTRAINT webhook_one_owner CHECK ((repository_id IS NULL) <> (group_id IS NULL));`,
		}
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	queries = append(queries, `CREATE INDEX idx_webhook_group ON webhook(group_id);`)
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downGroupWebhooks(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	queries := []string{
		`DELETE FROM webhook_delivery WHERE webhook_id IN (SELECT id FROM webhook WHERE group_id IS NOT NULL);`,
		`DELETE FROM webhook WHERE group_id IS NOT NULL;`,
		`DROP INDEX IF EXISTS idx_webhook_group;`,
	}
	if dialect == "sqlite" {
		// The CHECK names group_id: rebuild the table as it was.
		queries = append(queries,
			`CREATE TABLE webhook_old (
				id TEXT PRIMARY KEY,
				repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
				url TEXT NOT NULL,
				secret TEXT NOT NULL DEFAULT '',
				events TEXT NOT NULL DEFAULT '["push"]',
				active BOOLEAN NOT NULL DEFAULT TRUE,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
			`INSERT INTO webhook_old SELECT id, repository_id, url, secret, events, active, created_at, updated_at FROM webhook;`,
			`DROP TABLE webhook;`,
			`ALTER TABLE webhook_old RENAME TO webhook;`,
			`CREATE INDEX idx_webhook_repository ON webhook(repository_id);`)
	} else {
		queries = append(queries, `ALTER TABLE webhook DROP CONSTRAINT webhook_one_owner;`, `ALTER TABLE webhook DROP COLUMN group_id;`,
			`ALTER TABLE webhook ALTER COLUMN repository_id SET NOT NULL;`)
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
