package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upTokenUsage, downTokenUsage)
}

// When a token was last used, and who created a repository token.
func upTokenUsage(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE token ADD COLUMN last_used_at TIMESTAMP;
		ALTER TABLE token ADD COLUMN created_by_id TEXT REFERENCES user(id) ON DELETE SET NULL;
		`
	case "postgres":
		query = `
		ALTER TABLE token ADD COLUMN last_used_at TIMESTAMPTZ;
		ALTER TABLE token ADD COLUMN created_by_id UUID REFERENCES "user"(id) ON DELETE SET NULL;
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downTokenUsage(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE token DROP COLUMN created_by_id; ALTER TABLE token DROP COLUMN last_used_at;`)
	return err
}
