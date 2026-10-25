package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upMilestoneAssignee, downMilestoneAssignee)
}

// Milestones and assignees, for issues and pull requests, and the pull
// request whose merge closed an issue.
func upMilestoneAssignee(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE milestone (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			due_on TIMESTAMP,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed')),
			closed_at TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(repository_id, title)
		);
		ALTER TABLE issue ADD COLUMN milestone_id TEXT REFERENCES milestone(id) ON DELETE SET NULL;
		ALTER TABLE issue ADD COLUMN closed_by_pull INTEGER;
		ALTER TABLE pull_request ADD COLUMN milestone_id TEXT REFERENCES milestone(id) ON DELETE SET NULL;
		CREATE INDEX idx_issue_milestone ON issue(milestone_id);
		CREATE INDEX idx_pull_request_milestone ON pull_request(milestone_id);

		CREATE TABLE issue_assignee (
			issue_id TEXT NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, user_id)
		);
		CREATE INDEX idx_issue_assignee_user ON issue_assignee(user_id);
		CREATE TABLE pull_request_assignee (
			pull_request_id TEXT NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			PRIMARY KEY (pull_request_id, user_id)
		);
		`
	case "postgres":
		query = `
		CREATE TABLE milestone (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			due_on TIMESTAMPTZ,
			state TEXT NOT NULL CHECK(state IN ('open', 'closed')),
			closed_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(repository_id, title)
		);
		ALTER TABLE issue ADD COLUMN milestone_id UUID REFERENCES milestone(id) ON DELETE SET NULL;
		ALTER TABLE issue ADD COLUMN closed_by_pull INTEGER;
		ALTER TABLE pull_request ADD COLUMN milestone_id UUID REFERENCES milestone(id) ON DELETE SET NULL;
		CREATE INDEX idx_issue_milestone ON issue(milestone_id);
		CREATE INDEX idx_pull_request_milestone ON pull_request(milestone_id);

		CREATE TABLE issue_assignee (
			issue_id UUID NOT NULL REFERENCES issue(id) ON DELETE CASCADE,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			PRIMARY KEY (issue_id, user_id)
		);
		CREATE INDEX idx_issue_assignee_user ON issue_assignee(user_id);
		CREATE TABLE pull_request_assignee (
			pull_request_id UUID NOT NULL REFERENCES pull_request(id) ON DELETE CASCADE,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			PRIMARY KEY (pull_request_id, user_id)
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downMilestoneAssignee(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		DROP TABLE IF EXISTS pull_request_assignee;
		DROP TABLE IF EXISTS issue_assignee;
		DROP INDEX IF EXISTS idx_pull_request_milestone;
		DROP INDEX IF EXISTS idx_issue_milestone;
		ALTER TABLE pull_request DROP COLUMN milestone_id;
		ALTER TABLE issue DROP COLUMN closed_by_pull;
		ALTER TABLE issue DROP COLUMN milestone_id;
		DROP TABLE IF EXISTS milestone;`)
	return err
}
