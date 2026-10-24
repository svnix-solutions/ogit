package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSSHKeyRework, downSSHKeyRework)
}

// The original ssh_key table stored a private key, had no repository column
// for deploy keys and required key names to be unique site-wide. Nothing
// ever wrote to it, so it is recreated:
//   - only the public key (authorized_keys format) and its SHA256
//     fingerprint, which is unique: each key is used in one place;
//   - user keys (user_id) or deploy keys (repository_id, read_only);
//   - names unique per user / per repository.
func upSSHKeyRework(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		DROP TABLE IF EXISTS ssh_key;
		CREATE TABLE ssh_key (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			fingerprint TEXT NOT NULL UNIQUE,
			public_key TEXT NOT NULL,
			key_type TEXT NOT NULL CHECK(key_type IN ('rsa', 'ed25519', 'ecdsa')),
			owner_type TEXT NOT NULL CHECK(owner_type IN ('user', 'repository')),
			user_id TEXT,
			repository_id TEXT,
			read_only BOOLEAN NOT NULL DEFAULT TRUE,
			created_by TEXT,
			last_used_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			CHECK (
				(owner_type = 'user' AND user_id IS NOT NULL AND repository_id IS NULL) OR
				(owner_type = 'repository' AND repository_id IS NOT NULL AND user_id IS NULL)
			),
			FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE,
			FOREIGN KEY (repository_id) REFERENCES repository(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS unique_ssh_key_user_name ON ssh_key(user_id, name) WHERE user_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS unique_ssh_key_repository_name ON ssh_key(repository_id, name) WHERE repository_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_ssh_key_repository ON ssh_key(repository_id);
		`
	case "postgres":
		// Reuses the enum types created by the original ssh_key migration.
		query = `
		DROP TABLE IF EXISTS ssh_key;
		CREATE TABLE ssh_key (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL,
			fingerprint TEXT NOT NULL UNIQUE,
			public_key TEXT NOT NULL,
			key_type ssh_key_type_enum NOT NULL,
			owner_type ssh_key_owner_type_enum NOT NULL,
			user_id UUID REFERENCES "user"(id) ON DELETE CASCADE,
			repository_id UUID REFERENCES repository(id) ON DELETE CASCADE,
			read_only BOOLEAN NOT NULL DEFAULT TRUE,
			created_by UUID,
			last_used_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			CONSTRAINT chk_ssh_key_owner CHECK (
				(owner_type = 'user' AND user_id IS NOT NULL AND repository_id IS NULL) OR
				(owner_type = 'repository' AND repository_id IS NOT NULL AND user_id IS NULL)
			)
		);
		CREATE UNIQUE INDEX IF NOT EXISTS unique_ssh_key_user_name ON ssh_key(user_id, name) WHERE user_id IS NOT NULL;
		CREATE UNIQUE INDEX IF NOT EXISTS unique_ssh_key_repository_name ON ssh_key(repository_id, name) WHERE repository_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_ssh_key_repository ON ssh_key(repository_id);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}

// downSSHKeyRework restores the original (empty) table.
func downSSHKeyRework(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		DROP TABLE IF EXISTS ssh_key;
		CREATE TABLE ssh_key (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			public_key TEXT NOT NULL UNIQUE,
			private_key TEXT NOT NULL,
			user_id TEXT NOT NULL,
			owner_type TEXT NOT NULL CHECK(owner_type IN ('user', 'repository')),
			type TEXT NOT NULL CHECK(type IN ('rsa', 'ed25519', 'ecdsa')),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
		);
		`
	case "postgres":
		query = `
		DROP TABLE IF EXISTS ssh_key;
		CREATE TABLE ssh_key (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			name TEXT NOT NULL UNIQUE,
			public_key TEXT NOT NULL UNIQUE,
			private_key TEXT NOT NULL,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			owner_type ssh_key_owner_type_enum NOT NULL,
			type ssh_key_type_enum NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}
