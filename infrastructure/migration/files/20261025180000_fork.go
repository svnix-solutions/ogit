package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upFork, downFork)
}

// Forks (repository.fork_of_id) and pull requests from them
// (pull_request.source_repository_id, allow_maintainer_edits). The source
// repository has no foreign key: NULL means "this repository", so a deleted
// fork must stay a dangling id, not become NULL.
func upFork(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE repository ADD COLUMN fork_of_id TEXT REFERENCES repository(id) ON DELETE SET NULL;
		ALTER TABLE pull_request ADD COLUMN source_repository_id TEXT;
		ALTER TABLE pull_request ADD COLUMN allow_maintainer_edits BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE INDEX idx_repository_fork_of ON repository(fork_of_id);
		CREATE INDEX idx_pull_request_source_repository ON pull_request(source_repository_id, state);
		`
	case "postgres":
		query = `
		ALTER TABLE repository ADD COLUMN fork_of_id UUID REFERENCES repository(id) ON DELETE SET NULL;
		ALTER TABLE pull_request ADD COLUMN source_repository_id UUID;
		ALTER TABLE pull_request ADD COLUMN allow_maintainer_edits BOOLEAN NOT NULL DEFAULT FALSE;
		CREATE INDEX idx_repository_fork_of ON repository(fork_of_id);
		CREATE INDEX idx_pull_request_source_repository ON pull_request(source_repository_id, state);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downFork(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP INDEX IF EXISTS idx_pull_request_source_repository;
		DROP INDEX IF EXISTS idx_repository_fork_of;
		ALTER TABLE pull_request DROP COLUMN allow_maintainer_edits;
		ALTER TABLE pull_request DROP COLUMN source_repository_id;
		ALTER TABLE repository DROP COLUMN fork_of_id;`)
	return err
}
