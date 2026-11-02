package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWorkspaceBrowser, downWorkspaceBrowser)
}

// Where the server reaches an agent, with its certificate's fingerprint, and
// the one-time links that sign a browser in to a workspace.
func upWorkspaceBrowser(ctx context.Context, tx *sql.Tx) error {
	timeType := "TIMESTAMP"
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
	case "postgres":
		timeType = "TIMESTAMPTZ"
	default:
		return fmt.Errorf("unsupported dialect %q", dialect)
	}
	for _, statement := range []string{
		`ALTER TABLE workspace_agent ADD COLUMN proxy_url TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE workspace_agent ADD COLUMN proxy_fingerprint TEXT NOT NULL DEFAULT '';`,
		fmt.Sprintf(`CREATE TABLE workspace_ticket (
			token_hash TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			port INTEGER NOT NULL,
			expires_at %s NOT NULL
		);`, timeType),
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func downWorkspaceBrowser(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`DROP TABLE workspace_ticket;`,
		`ALTER TABLE workspace_agent DROP COLUMN proxy_fingerprint;`,
		`ALTER TABLE workspace_agent DROP COLUMN proxy_url;`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
