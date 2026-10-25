package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upIssue, downIssue)
}

// Issues, their comments and labels; issues share one number sequence with
// pull requests (repository.last_number), and can be turned off per
// repository.
func upIssue(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		ALTER TABLE repository ADD COLUMN last_number INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE repository ADD COLUMN has_issues BOOLEAN NOT NULL DEFAULT TRUE;
		UPDATE repository SET last_number = COALESCE((SELECT MAX(number) FROM pull_request WHERE repository_id = repository.id), 0);

		CREATE TABLE issue (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			author_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed')),
			closed_at TIMESTAMP,
			closed_by_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, number)
		);
		CREATE INDEX idx_issue_list ON issue(repository_id, state, created_at);

		CREATE TABLE issue_comment (
			id TEXT PRIMARY KEY,
			issue_id TEXT NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			author_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			body TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_issue_comment_issue ON issue_comment(issue_id, created_at);

		CREATE TABLE issue_label (
			issue_id TEXT NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			label_id TEXT NOT NULL REFERENCES label(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, label_id)
		);
		`
	case "postgres":
		query = `
		ALTER TABLE repository ADD COLUMN last_number INTEGER NOT NULL DEFAULT 0;
		ALTER TABLE repository ADD COLUMN has_issues BOOLEAN NOT NULL DEFAULT TRUE;
		UPDATE repository SET last_number = COALESCE((SELECT MAX(number) FROM pull_request WHERE repository_id = repository.id), 0);

		CREATE TABLE issue (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			number INTEGER NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed')),
			closed_at TIMESTAMPTZ,
			closed_by_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, number)
		);
		CREATE INDEX idx_issue_list ON issue(repository_id, state, created_at);

		CREATE TABLE issue_comment (
			id UUID PRIMARY KEY,
			issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			author_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			body TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_issue_comment_issue ON issue_comment(issue_id, created_at);

		CREATE TABLE issue_label (
			issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			label_id UUID NOT NULL REFERENCES label(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, label_id)
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downIssue(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS issue_label;
		DROP TABLE IF EXISTS issue_comment;
		DROP TABLE IF EXISTS issue;
		ALTER TABLE repository DROP COLUMN has_issues;
		ALTER TABLE repository DROP COLUMN last_number;`)
	return err
}
