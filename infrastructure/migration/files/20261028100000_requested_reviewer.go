package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upRequestedReviewer, downRequestedReviewer)
}

// Reviews requested from users on pull requests.
func upRequestedReviewer(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE pull_request_reviewer (
			pull_request_id TEXT NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			requested_by_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (pull_request_id, user_id)
		);
		CREATE INDEX idx_pull_request_reviewer_user ON pull_request_reviewer(user_id);
		`
	case "postgres":
		query = `
		CREATE TABLE pull_request_reviewer (
			pull_request_id UUID NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			requested_by_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (pull_request_id, user_id)
		);
		CREATE INDEX idx_pull_request_reviewer_user ON pull_request_reviewer(user_id);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downRequestedReviewer(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS pull_request_reviewer;`)
	return err
}
