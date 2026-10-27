package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upGroupRedirect, downGroupRedirect)
}

// group_redirect maps a former group path (after a sub-group was renamed
// or moved) to the group, like repository_redirect for repositories.
func upGroupRedirect(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	var q string
	switch dialect {
	case "sqlite":
		q = `CREATE TABLE group_redirect (
			path TEXT PRIMARY KEY,
			group_id TEXT NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`
	case "postgres":
		q = `CREATE TABLE group_redirect (
			path TEXT PRIMARY KEY,
			group_id UUID NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	if _, err := tx.ExecContext(ctx, q); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `CREATE INDEX idx_group_redirect_group ON group_redirect(group_id);`)
	return err
}

func downGroupRedirect(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS group_redirect;`)
	return err
}
