package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSSO, downSSO)
}

// Single sign-on: providers (OIDC, GitHub), identities linking users to
// accounts there, sign-ins in progress, links waiting for a password, and
// the provider a session came through.
func upSSO(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	ts, id := "TIMESTAMP", "TEXT"
	switch dialect {
	case "sqlite":
	case "postgres":
		ts, id = "TIMESTAMPTZ", "UUID"
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	for _, q := range []string{
		`CREATE TABLE sso_provider (
			id ` + id + ` PRIMARY KEY,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('oidc', 'github')),
			issuer TEXT NOT NULL,
			client_id TEXT NOT NULL,
			client_secret TEXT NOT NULL DEFAULT '',
			scopes TEXT NOT NULL DEFAULT '',
			allowed_domains TEXT NOT NULL DEFAULT '',
			allowed_orgs TEXT NOT NULL DEFAULT '',
			auto_create BOOLEAN NOT NULL DEFAULT TRUE,
			require_two_factor BOOLEAN NOT NULL DEFAULT FALSE,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at ` + ts + ` NOT NULL,
			updated_at ` + ts + ` NOT NULL
		);`,
		`CREATE TABLE user_identity (
			id ` + id + ` PRIMARY KEY,
			user_id ` + id + ` NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			subject TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			last_login_at ` + ts + `,
			created_at ` + ts + ` NOT NULL,
			UNIQUE (provider, subject)
		);`,
		`CREATE INDEX idx_user_identity_user ON user_identity(user_id);`,
		`CREATE TABLE sso_flow (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			purpose TEXT NOT NULL CHECK (purpose IN ('login', 'link', 'sudo')),
			browser_key TEXT NOT NULL,
			nonce TEXT NOT NULL,
			verifier TEXT NOT NULL,
			redirect TEXT NOT NULL DEFAULT '',
			user_id ` + id + `,
			session_id ` + id + `,
			expires_at ` + ts + ` NOT NULL,
			created_at ` + ts + ` NOT NULL
		);`,
		`CREATE TABLE sso_pending_link (
			id TEXT PRIMARY KEY,
			user_id ` + id + ` NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			provider TEXT NOT NULL,
			subject TEXT NOT NULL,
			email TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			redirect TEXT NOT NULL DEFAULT '',
			expires_at ` + ts + ` NOT NULL,
			created_at ` + ts + ` NOT NULL
		);`,
		`ALTER TABLE session ADD COLUMN sso_provider TEXT;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downSSO(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS sso_pending_link;`, `DROP TABLE IF EXISTS sso_flow;`, `DROP TABLE IF EXISTS user_identity;`,
		`DROP TABLE IF EXISTS sso_provider;`, `ALTER TABLE session DROP COLUMN sso_provider;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
