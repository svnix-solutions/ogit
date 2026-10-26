package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upActionCache, downActionCache)
}

// The Actions cache (actions/cache): entries per repository, ref (scope),
// key and version, stored in the repository's "actions-cache" area, and
// the chunks of uploads in progress.
func upActionCache(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	ts, big := "TIMESTAMP", "INTEGER"
	switch dialect {
	case "sqlite":
	case "postgres":
		ts, big = "TIMESTAMPTZ", "BIGINT"
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	for _, q := range []string{
		`CREATE TABLE action_cache (
			id TEXT PRIMARY KEY,
			api_id ` + big + ` NOT NULL UNIQUE,
			repository_id TEXT NOT NULL,
			scope TEXT NOT NULL,
			cache_key TEXT NOT NULL,
			version TEXT NOT NULL,
			size ` + big + ` NOT NULL DEFAULT 0,
			complete BOOLEAN NOT NULL DEFAULT FALSE,
			created_at ` + ts + ` NOT NULL,
			last_used_at ` + ts + ` NOT NULL,
			UNIQUE(repository_id, scope, cache_key, version)
		);`,
		`CREATE INDEX idx_action_cache_lookup ON action_cache(repository_id, version, scope);`,
		`CREATE INDEX idx_action_cache_used ON action_cache(last_used_at);`,
		`CREATE TABLE action_cache_chunk (
			cache_id TEXT NOT NULL,
			start ` + big + ` NOT NULL,
			size ` + big + ` NOT NULL,
			PRIMARY KEY (cache_id, start)
		);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downActionCache(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{`DROP TABLE IF EXISTS action_cache_chunk;`, `DROP TABLE IF EXISTS action_cache;`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
