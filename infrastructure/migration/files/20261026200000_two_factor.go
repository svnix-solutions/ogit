package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upTwoFactor, downTwoFactor)
}

// Two-factor authentication: TOTP secrets (encrypted), recovery codes
// (hashed), pending logins between the password and the code, and the
// session's sudo window.
func upTwoFactor(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE user_totp (
			user_id TEXT PRIMARY KEY REFERENCES user(id) ON DELETE CASCADE,
			secret TEXT NOT NULL,
			confirmed_at TIMESTAMP,
			last_step BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE recovery_code (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			code_hash TEXT NOT NULL,
			used_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_recovery_code_user ON recovery_code(user_id);
		CREATE TABLE login_challenge (
			id TEXT PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			attempts INTEGER NOT NULL DEFAULT 0,
			user_agent TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			expires_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		ALTER TABLE session ADD COLUMN sudo_until TIMESTAMP;
		`
	case "postgres":
		query = `
		CREATE TABLE user_totp (
			user_id UUID PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
			secret TEXT NOT NULL,
			confirmed_at TIMESTAMPTZ,
			last_step BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE TABLE recovery_code (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			code_hash TEXT NOT NULL,
			used_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_recovery_code_user ON recovery_code(user_id);
		CREATE TABLE login_challenge (
			id UUID PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			attempts INTEGER NOT NULL DEFAULT 0,
			user_agent TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		ALTER TABLE session ADD COLUMN sudo_until TIMESTAMPTZ;
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downTwoFactor(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS login_challenge; DROP TABLE IF EXISTS recovery_code; DROP TABLE IF EXISTS user_totp;
		ALTER TABLE session DROP COLUMN sudo_until;`)
	return err
}
