package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upGroupMember, downGroupMember)
}

// group_member holds group membership. The group migration only had an
// unused "members" column, while the domain model expected a join table
// that no migration created.
//
// Foreign keys cascade on postgres; sqlite does not enforce them here
// (foreign_keys pragma is off), so the application deletes dependent rows
// itself.
func upGroupMember(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE IF NOT EXISTS group_member (
			id TEXT PRIMARY KEY,
			group_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			role TEXT NOT NULL CHECK(role IN ('member', 'maintainer', 'owner')),
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (group_id) REFERENCES "group"(id) ON DELETE CASCADE,
			FOREIGN KEY (user_id) REFERENCES user(id) ON DELETE CASCADE
		);

		CREATE UNIQUE INDEX IF NOT EXISTS unique_group_member ON group_member(group_id, user_id);
		CREATE INDEX IF NOT EXISTS idx_group_member_user ON group_member(user_id);
		`
	case "postgres":
		query = `
		DO $$ BEGIN
			CREATE TYPE group_member_role_enum AS ENUM ('member', 'maintainer', 'owner');
		EXCEPTION
			WHEN duplicate_object THEN null;
		END $$;

		CREATE TABLE IF NOT EXISTS group_member (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			group_id UUID NOT NULL REFERENCES "group"(id) ON DELETE CASCADE,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			role group_member_role_enum NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		CREATE UNIQUE INDEX IF NOT EXISTS unique_group_member ON group_member(group_id, user_id);
		CREATE INDEX IF NOT EXISTS idx_group_member_user ON group_member(user_id);

		DROP TRIGGER IF EXISTS update_group_member_updated_at ON group_member;
		CREATE TRIGGER update_group_member_updated_at
			BEFORE UPDATE ON group_member
			FOR EACH ROW
			EXECUTE FUNCTION update_updated_at_column();
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}

func downGroupMember(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)

	var query string
	switch dialect {
	case "sqlite":
		query = `DROP TABLE IF EXISTS group_member;`
	case "postgres":
		query = `
		DROP TABLE IF EXISTS group_member;
		DROP TYPE IF EXISTS group_member_role_enum;
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}

	_, err := tx.ExecContext(ctx, query)
	return err
}
