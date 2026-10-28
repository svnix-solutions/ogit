package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upNamespaces, downNamespaces)
}

// Repositories move from one flat namespace (/<slug>.git) to owner
// namespaces (/<user or group>/<slug>.git):
//   - repository gets namespace (the owner's username or group name),
//     owner_type and owner_id; slugs and names become unique per namespace
//     instead of globally.
//   - Existing repositories move under their oldest owner (a user: groups
//     can't be owners). Their old flat URLs stop working.
//   - repository_redirect is keyed by (namespace, slug); flat redirects are
//     dropped.
func upNamespaces(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)

	// owner_id: the oldest user owner of each repository.
	ownerOf := `(SELECT rm.user_id FROM repository_member rm
		WHERE rm.repository_id = repository.id AND rm.member_type = 'user' AND rm.access_type = 'owner'
		ORDER BY rm.created_at, rm.id LIMIT 1)`
	if err := checkEveryRepositoryHasOwner(ctx, tx, ownerOf); err != nil {
		return err
	}

	var query string
	switch dialect {
	case "sqlite":
		// SQLite can't drop the inline UNIQUE on slug: rebuild the table.
		// Foreign keys are not enforced here, and references by name
		// ("repository") resolve to the rebuilt table.
		query = `
		CREATE TABLE repository_new (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			namespace TEXT NOT NULL,
			owner_type TEXT NOT NULL CHECK(owner_type IN ('user', 'group')),
			owner_id TEXT NOT NULL,
			slug TEXT NOT NULL,
			storage_key TEXT NOT NULL,
			default_branch TEXT NOT NULL DEFAULT 'main',
			is_archived BOOLEAN DEFAULT FALSE,
			archived_at TIMESTAMP,
			is_private BOOLEAN DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO repository_new (id, name, description, namespace, owner_type, owner_id, slug, storage_key,
			default_branch, is_archived, archived_at, is_private, created_at, updated_at)
		SELECT id, name, description,
			(SELECT username FROM user WHERE user.id = ` + ownerOf + `),
			'user', ` + ownerOf + `, slug, storage_key,
			default_branch, is_archived, archived_at, is_private, created_at, updated_at
		FROM repository;
		DROP TABLE repository;
		ALTER TABLE repository_new RENAME TO repository;
		CREATE UNIQUE INDEX idx_repository_namespace_slug ON repository(namespace, slug);
		CREATE INDEX idx_repository_owner ON repository(owner_type, owner_id);
		CREATE UNIQUE INDEX idx_repository_storage_key ON repository(storage_key);
		CREATE INDEX idx_repository_is_private ON repository(is_private);
		CREATE INDEX idx_repository_is_archived ON repository(is_archived);

		DROP TABLE repository_redirect;
		CREATE TABLE repository_redirect (
			namespace TEXT NOT NULL,
			slug TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (namespace, slug),
			FOREIGN KEY (repository_id) REFERENCES repository(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	case "postgres":
		pgOwner := strings.ReplaceAll(ownerOf, "SELECT rm.user_id", "SELECT rm.user_id::text")
		query = `
		ALTER TABLE repository ADD COLUMN namespace TEXT;
		ALTER TABLE repository ADD COLUMN owner_type TEXT;
		ALTER TABLE repository ADD COLUMN owner_id TEXT;
		UPDATE repository SET owner_type = 'user', owner_id = ` + pgOwner + `;
		UPDATE repository SET namespace = (SELECT username FROM "user" WHERE "user".id::text = repository.owner_id);
		ALTER TABLE repository ALTER COLUMN namespace SET NOT NULL;
		ALTER TABLE repository ALTER COLUMN owner_type SET NOT NULL;
		ALTER TABLE repository ALTER COLUMN owner_id SET NOT NULL;
		ALTER TABLE repository ADD CONSTRAINT repository_owner_type_check CHECK (owner_type IN ('user', 'group'));
		ALTER TABLE repository DROP CONSTRAINT IF EXISTS repository_slug_key;
		DROP INDEX IF EXISTS idx_repository_slug;
		DROP INDEX IF EXISTS idx_repository_name;
		CREATE UNIQUE INDEX idx_repository_namespace_slug ON repository(namespace, slug);
		CREATE INDEX idx_repository_owner ON repository(owner_type, owner_id);

		DROP TABLE repository_redirect;
		CREATE TABLE repository_redirect (
			namespace TEXT NOT NULL,
			slug TEXT NOT NULL,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (namespace, slug)
		);
		CREATE INDEX idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}

// checkEveryRepositoryHasOwner fails the migration, naming the repositories,
// instead of letting NOT NULL fail without saying which ones. Repositories
// always keep one user owner, so this only catches damaged data.
func checkEveryRepositoryHasOwner(ctx context.Context, tx *sql.Tx, ownerOf string) error {
	rows, err := tx.QueryContext(ctx, `SELECT slug FROM repository WHERE `+ownerOf+` IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var orphans []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return err
		}
		orphans = append(orphans, slug)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(orphans) > 0 {
		return fmt.Errorf("repositories without a user owner can't be given a namespace: %s; add an owner (ogit admin or the API) and migrate again", strings.Join(orphans, ", "))
	}
	return nil
}

// downNamespaces restores the flat layout. It fails if two namespaces hold a
// repository with the same slug or name.
func downNamespaces(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)

	var query string
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE repository_old (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			slug TEXT NOT NULL UNIQUE,
			storage_key TEXT,
			default_branch TEXT NOT NULL DEFAULT 'main',
			is_archived BOOLEAN DEFAULT FALSE,
			archived_at TIMESTAMP,
			is_private BOOLEAN DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO repository_old (id, name, description, slug, storage_key, default_branch, is_archived, archived_at, is_private, created_at, updated_at)
		SELECT id, name, description, slug, storage_key, default_branch, is_archived, archived_at, is_private, created_at, updated_at FROM repository;
		DROP TABLE repository;
		ALTER TABLE repository_old RENAME TO repository;
		CREATE UNIQUE INDEX idx_repository_slug ON repository(slug);
		CREATE UNIQUE INDEX idx_repository_name ON repository(name);
		CREATE UNIQUE INDEX idx_repository_storage_key ON repository(storage_key);
		CREATE INDEX idx_repository_is_private ON repository(is_private);
		CREATE INDEX idx_repository_is_archived ON repository(is_archived);

		DROP TABLE repository_redirect;
		CREATE TABLE repository_redirect (
			slug TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (repository_id) REFERENCES repository(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	case "postgres":
		query = `
		DROP INDEX IF EXISTS idx_repository_namespace_slug;
		DROP INDEX IF EXISTS idx_repository_owner;
		ALTER TABLE repository DROP CONSTRAINT IF EXISTS repository_owner_type_check;
		ALTER TABLE repository DROP COLUMN namespace;
		ALTER TABLE repository DROP COLUMN owner_type;
		ALTER TABLE repository DROP COLUMN owner_id;
		CREATE UNIQUE INDEX idx_repository_slug ON repository(slug);
		CREATE UNIQUE INDEX idx_repository_name ON repository(name);

		DROP TABLE repository_redirect;
		CREATE TABLE repository_redirect (
			slug TEXT PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_repository_redirect_repository ON repository_redirect(repository_id);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}
