package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upEmailVerification, downEmailVerification)
}

// Email verification: user.email_verified_at (NULL = unverified) and one
// pending verification per user. Users that exist when this migration runs
// are marked verified, so enabling verification never locks anyone out.
func upEmailVerification(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE user ADD COLUMN email_verified_at TIMESTAMP;
		UPDATE user SET email_verified_at = CURRENT_TIMESTAMP;

		CREATE TABLE IF NOT EXISTS email_verification (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL UNIQUE,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
		);
		`
	case "postgres":
		query = `
		ALTER TABLE "user" ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;
		UPDATE "user" SET email_verified_at = NOW() WHERE email_verified_at IS NULL;

		CREATE TABLE IF NOT EXISTS email_verification (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			user_id UUID NOT NULL UNIQUE REFERENCES "user"(id) ON DELETE CASCADE,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}

func downEmailVerification(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)

	var query string
	switch dialect {
	case "sqlite":
		query = `
		DROP TABLE IF EXISTS email_verification;
		ALTER TABLE user DROP COLUMN email_verified_at;
		`
	case "postgres":
		query = `
		DROP TABLE IF EXISTS email_verification;
		ALTER TABLE "user" DROP COLUMN IF EXISTS email_verified_at;
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}
