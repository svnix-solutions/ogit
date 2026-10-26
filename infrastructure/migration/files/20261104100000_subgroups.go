package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSubgroups, downSubgroups)
}

// Sub-groups: a group may have a parent group. Its name is its full path
// (org/team), unique like top-level names, so name lookups and the
// repositories' namespace column work unchanged.
func upSubgroups(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	var queries []string
	switch dialect {
	case "sqlite":
		queries = []string{`ALTER TABLE "group" ADD COLUMN parent_id TEXT REFERENCES "group"(id);`}
	case "postgres":
		queries = []string{`ALTER TABLE "group" ADD COLUMN parent_id UUID REFERENCES "group"(id);`}
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	queries = append(queries, `CREATE INDEX idx_group_parent ON "group"(parent_id);`)
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downSubgroups(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{`DROP INDEX IF EXISTS idx_group_parent;`, `ALTER TABLE "group" DROP COLUMN parent_id;`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
