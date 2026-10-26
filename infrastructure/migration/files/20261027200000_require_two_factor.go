package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRequireTwoFactor, downRequireTwoFactor)
}

// Requiring two-factor authentication: per group, and site-wide (site
// settings changed from the admin page).
func upRequireTwoFactor(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE "group" ADD COLUMN require_two_factor BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE TABLE site_setting (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		`
	case "postgres":
		query = `
		ALTER TABLE "group" ADD COLUMN require_two_factor BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE TABLE site_setting (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downRequireTwoFactor(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS site_setting; ALTER TABLE "group" DROP COLUMN require_two_factor;`)
	return err
}
