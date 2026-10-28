package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upPullRequest, downPullRequest)
}

func upPullRequest(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE pull_request (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			author_id TEXT,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed', 'merged')),
			draft BOOLEAN NOT NULL DEFAULT FALSE,
			base_commit TEXT NOT NULL DEFAULT '',
			head_commit TEXT NOT NULL DEFAULT '',
			merge_commit TEXT NOT NULL DEFAULT '',
			merge_method TEXT NOT NULL DEFAULT '',
			merged_by_id TEXT,
			merged_at TIMESTAMP,
			closed_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX idx_pull_request_number ON pull_request(repository_id, number);
		CREATE INDEX idx_pull_request_state ON pull_request(repository_id, state);
		`
	case "postgres":
		query = `
		CREATE TABLE pull_request (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			source_branch TEXT NOT NULL,
			target_branch TEXT NOT NULL,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed', 'merged')),
			draft BOOLEAN NOT NULL DEFAULT FALSE,
			base_commit TEXT NOT NULL DEFAULT '',
			head_commit TEXT NOT NULL DEFAULT '',
			merge_commit TEXT NOT NULL DEFAULT '',
			merge_method TEXT NOT NULL DEFAULT '',
			merged_by_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			merged_at TIMESTAMPTZ,
			closed_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX idx_pull_request_number ON pull_request(repository_id, number);
		CREATE INDEX idx_pull_request_state ON pull_request(repository_id, state);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downPullRequest(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS pull_request`)
	return err
}
