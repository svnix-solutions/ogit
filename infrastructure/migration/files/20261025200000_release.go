package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRelease, downRelease)
}

// Releases, one per tag of a repository.
func upRelease(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE repository_release (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			tag_name TEXT NOT NULL,
			target_commitish TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			draft BOOLEAN NOT NULL DEFAULT FALSE,
			prerelease BOOLEAN NOT NULL DEFAULT FALSE,
			author_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			published_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, tag_name)
		);
		CREATE INDEX idx_release_list ON repository_release(repository_id, created_at);
		`
	case "postgres":
		query = `
		CREATE TABLE repository_release (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			tag_name TEXT NOT NULL,
			target_commitish TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '',
			draft BOOLEAN NOT NULL DEFAULT FALSE,
			prerelease BOOLEAN NOT NULL DEFAULT FALSE,
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			published_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, tag_name)
		);
		CREATE INDEX idx_release_list ON repository_release(repository_id, created_at);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downRelease(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS repository_release;`)
	return err
}
