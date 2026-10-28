package migrations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upReleaseAssets, downReleaseAssets)
}

// Release assets, numeric ids for the GitHub-compatible API (its clients
// parse ids as integers), and protected tags.
func upReleaseAssets(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	update := `UPDATE repository_release SET api_id = ? WHERE id = ?`
	if dialect == "postgres" {
		update = `UPDATE repository_release SET api_id = $1 WHERE id = $2`
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE repository_release ADD COLUMN api_id BIGINT;`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM repository_release`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		n := int64(binary.BigEndian.Uint64(b[:])&(1<<53-1)) | 1
		if _, err := tx.ExecContext(ctx, update, n, id); err != nil {
			return err
		}
	}

	var query string
	switch dialect {
	case "sqlite":
		query = `
		CREATE UNIQUE INDEX idx_release_api_id ON repository_release(api_id);
		CREATE TABLE release_asset (
			id TEXT PRIMARY KEY,
			api_id BIGINT NOT NULL UNIQUE,
			release_id TEXT NOT NULL REFERENCES repository_release(id) ON DELETE CASCADE,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
			size BIGINT NOT NULL DEFAULT 0,
			download_count BIGINT NOT NULL DEFAULT 0,
			uploader_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(release_id, name)
		);
		CREATE TABLE protected_tag (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			pattern TEXT NOT NULL,
			allowed_user_ids TEXT NOT NULL DEFAULT '[]',
			allowed_group_ids TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, pattern)
		);
		`
	case "postgres":
		query = `
		CREATE UNIQUE INDEX idx_release_api_id ON repository_release(api_id);
		CREATE TABLE release_asset (
			id UUID PRIMARY KEY,
			api_id BIGINT NOT NULL UNIQUE,
			release_id UUID NOT NULL REFERENCES repository_release(id) ON DELETE CASCADE,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
			size BIGINT NOT NULL DEFAULT 0,
			download_count BIGINT NOT NULL DEFAULT 0,
			uploader_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(release_id, name)
		);
		CREATE TABLE protected_tag (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			pattern TEXT NOT NULL,
			allowed_user_ids TEXT NOT NULL DEFAULT '[]',
			allowed_group_ids TEXT NOT NULL DEFAULT '[]',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, pattern)
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err = tx.ExecContext(ctx, query)
	return err
}

func downReleaseAssets(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS protected_tag; DROP TABLE IF EXISTS release_asset; DROP INDEX IF EXISTS idx_release_api_id;
		ALTER TABLE repository_release DROP COLUMN api_id;`)
	return err
}
