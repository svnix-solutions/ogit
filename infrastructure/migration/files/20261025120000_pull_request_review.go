package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upPullRequestReview, downPullRequestReview)
}

// Reviews, comments, labels, and the approvals a repository requires before
// a pull request can be merged.
func upPullRequestReview(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE repository ADD COLUMN required_approvals INTEGER NOT NULL DEFAULT 0;

		CREATE TABLE pull_request_review (
			id TEXT PRIMARY KEY,
			pull_request_id TEXT NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			author_id TEXT,
			state TEXT NOT NULL CHECK(state IN ('approved', 'changes_requested', 'commented')),
			body TEXT NOT NULL DEFAULT '',
			commit_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_pull_request_review_pr ON pull_request_review(pull_request_id);

		CREATE TABLE pull_request_comment (
			id TEXT PRIMARY KEY,
			pull_request_id TEXT NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			author_id TEXT,
			review_id TEXT REFERENCES pull_request_review(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			line INTEGER NOT NULL DEFAULT 0,
			side TEXT NOT NULL DEFAULT '',
			commit_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_pull_request_comment_pr ON pull_request_comment(pull_request_id);

		CREATE TABLE label (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			color TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE UNIQUE INDEX idx_label_name ON label(repository_id, name);

		CREATE TABLE pull_request_label (
			pull_request_id TEXT NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			label_id TEXT NOT NULL REFERENCES label(id) ON DELETE CASCADE,
			PRIMARY KEY (pull_request_id, label_id)
		);
		`
	case "postgres":
		query = `
		ALTER TABLE repository ADD COLUMN required_approvals INTEGER NOT NULL DEFAULT 0;

		CREATE TABLE pull_request_review (
			id UUID PRIMARY KEY,
			pull_request_id UUID NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('approved', 'changes_requested', 'commented')),
			body TEXT NOT NULL DEFAULT '',
			commit_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_pull_request_review_pr ON pull_request_review(pull_request_id);

		CREATE TABLE pull_request_comment (
			id UUID PRIMARY KEY,
			pull_request_id UUID NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			review_id UUID REFERENCES pull_request_review(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			path TEXT NOT NULL DEFAULT '',
			line INTEGER NOT NULL DEFAULT 0,
			side TEXT NOT NULL DEFAULT '',
			commit_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_pull_request_comment_pr ON pull_request_comment(pull_request_id);

		CREATE TABLE label (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			color TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE UNIQUE INDEX idx_label_name ON label(repository_id, name);

		CREATE TABLE pull_request_label (
			pull_request_id UUID NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			label_id UUID NOT NULL REFERENCES label(id) ON DELETE CASCADE,
			PRIMARY KEY (pull_request_id, label_id)
		);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downPullRequestReview(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	query := `
		DROP TABLE IF EXISTS pull_request_label;
		DROP TABLE IF EXISTS label;
		DROP TABLE IF EXISTS pull_request_comment;
		DROP TABLE IF EXISTS pull_request_review;
		ALTER TABLE repository DROP COLUMN required_approvals;
	`
	if dialect != "sqlite" && dialect != "postgres" {
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}
