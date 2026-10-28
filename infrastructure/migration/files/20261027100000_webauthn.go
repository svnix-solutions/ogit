package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWebAuthn, downWebAuthn)
}

// Security keys and passkeys (WebAuthn credentials), and the challenges of
// their ceremonies (single use, 5 minutes).
func upWebAuthn(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE webauthn_credential (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			credential_id TEXT NOT NULL UNIQUE,
			passkey BOOLEAN NOT NULL DEFAULT FALSE,
			data TEXT NOT NULL,
			last_used_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, name)
		);
		CREATE TABLE webauthn_ceremony (
			id TEXT PRIMARY KEY,
			user_id TEXT REFERENCES user(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			data TEXT NOT NULL,
			expires_at TIMESTAMP NOT NULL
		);
		`
	case "postgres":
		query = `
		CREATE TABLE webauthn_credential (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			credential_id TEXT NOT NULL UNIQUE,
			passkey BOOLEAN NOT NULL DEFAULT FALSE,
			data TEXT NOT NULL,
			last_used_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(user_id, name)
		);
		CREATE TABLE webauthn_ceremony (
			id UUID PRIMARY KEY,
			user_id UUID REFERENCES "user"(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			data TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downWebAuthn(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS webauthn_ceremony; DROP TABLE IF EXISTS webauthn_credential;`)
	return err
}
