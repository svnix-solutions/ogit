package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upPackages, downPackages)
}

// Packages: owned by a namespace (user or group), optionally linked to a
// repository; versions with files (generic, npm, Go) or, for container
// images, manifests (versions named by digest) with tags; container blobs
// are content-addressed per namespace, with the blobs each manifest
// references (for garbage collection) and upload sessions.
func upPackages(ctx context.Context, tx *sql.Tx) error {
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
		`CREATE TABLE package (
			id TEXT PRIMARY KEY,
			owner_type TEXT NOT NULL CHECK (owner_type IN ('user', 'group')),
			owner_id TEXT NOT NULL,
			namespace TEXT NOT NULL,
			type TEXT NOT NULL CHECK (type IN ('container', 'npm', 'go', 'generic')),
			name TEXT NOT NULL,
			repository_id TEXT,
			visibility TEXT NOT NULL CHECK (visibility IN ('repository', 'public', 'private')),
			created_at ` + ts + ` NOT NULL,
			updated_at ` + ts + ` NOT NULL,
			UNIQUE(owner_id, type, name)
		);`,
		`CREATE INDEX idx_package_namespace ON package(namespace);`,
		`CREATE INDEX idx_package_repository ON package(repository_id);`,
		`CREATE TABLE package_version (
			id TEXT PRIMARY KEY,
			package_id TEXT NOT NULL,
			version TEXT NOT NULL,
			metadata TEXT NOT NULL DEFAULT '{}',
			size ` + big + ` NOT NULL DEFAULT 0,
			publisher_id TEXT,
			publisher_name TEXT NOT NULL DEFAULT '',
			created_at ` + ts + ` NOT NULL,
			UNIQUE(package_id, version)
		);`,
		`CREATE TABLE package_file (
			id TEXT PRIMARY KEY,
			version_id TEXT NOT NULL,
			name TEXT NOT NULL,
			size ` + big + ` NOT NULL,
			sha256 TEXT NOT NULL,
			content_type TEXT NOT NULL DEFAULT '',
			created_at ` + ts + ` NOT NULL,
			UNIQUE(version_id, name)
		);`,
		`CREATE TABLE container_tag (
			package_id TEXT NOT NULL,
			tag TEXT NOT NULL,
			digest TEXT NOT NULL,
			updated_at ` + ts + ` NOT NULL,
			PRIMARY KEY (package_id, tag)
		);`,
		`CREATE TABLE container_blob (
			owner_id TEXT NOT NULL,
			digest TEXT NOT NULL,
			size ` + big + ` NOT NULL,
			created_at ` + ts + ` NOT NULL,
			PRIMARY KEY (owner_id, digest)
		);`,
		`CREATE TABLE container_manifest_ref (
			version_id TEXT NOT NULL,
			digest TEXT NOT NULL,
			PRIMARY KEY (version_id, digest)
		);`,
		`CREATE INDEX idx_container_manifest_ref_digest ON container_manifest_ref(digest);`,
		`CREATE TABLE container_upload (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			size ` + big + ` NOT NULL DEFAULT 0,
			chunks INTEGER NOT NULL DEFAULT 0,
			created_at ` + ts + ` NOT NULL
		);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downPackages(ctx context.Context, tx *sql.Tx) error {
	for _, t := range []string{"container_upload", "container_manifest_ref", "container_blob", "container_tag", "package_file", "package_version", "package"} {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+t+`;`); err != nil {
			return err
		}
	}
	return nil
}
