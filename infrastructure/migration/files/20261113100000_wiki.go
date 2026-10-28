package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWiki, downWiki)
}

// Wikis: who edits a repository's wiki (its git data is a second
// repository, <storage key>.wiki).
func upWiki(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE repository ADD COLUMN wiki_access TEXT NOT NULL DEFAULT 'writers' CHECK (wiki_access IN ('writers', 'readers', 'off'));`)
	return err
}

func downWiki(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE repository DROP COLUMN wiki_access;`)
	return err
}
