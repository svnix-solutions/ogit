package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWorkspaceAccessLost, downWorkspaceAccessLost)
}

// When a workspace's user lost access to it: it's deleted a retention period
// later, unless they get it back first.
func upWorkspaceAccessLost(ctx context.Context, tx *sql.Tx) error {
	timeType := "TIMESTAMP"
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
	case "postgres":
		timeType = "TIMESTAMPTZ"
	default:
		return fmt.Errorf("unsupported dialect %q", dialect)
	}
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE workspace ADD COLUMN access_lost_at %s;`, timeType))
	return err
}

func downWorkspaceAccessLost(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `ALTER TABLE workspace DROP COLUMN access_lost_at;`)
	return err
}
