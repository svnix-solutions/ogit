package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSSOSync, downSSOSync)
}

// Group and admin sync from sign-in providers: what each provider maps,
// which memberships and admin roles sync made (only those it changes),
// and the groups of a sign-in waiting for its link.
func upSSOSync(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`ALTER TABLE sso_provider ADD COLUMN groups_claim TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE sso_provider ADD COLUMN admin_group TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE sso_provider ADD COLUMN group_mappings TEXT NOT NULL DEFAULT '[]';`,
		`ALTER TABLE sso_pending_link ADD COLUMN groups TEXT NOT NULL DEFAULT '[]';`,
		`ALTER TABLE "user" ADD COLUMN role_synced BOOLEAN NOT NULL DEFAULT FALSE;`,
		`ALTER TABLE group_member ADD COLUMN synced_from TEXT;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downSSOSync(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`ALTER TABLE group_member DROP COLUMN synced_from;`,
		`ALTER TABLE "user" DROP COLUMN role_synced;`,
		`ALTER TABLE sso_pending_link DROP COLUMN groups;`,
		`ALTER TABLE sso_provider DROP COLUMN group_mappings;`,
		`ALTER TABLE sso_provider DROP COLUMN admin_group;`,
		`ALTER TABLE sso_provider DROP COLUMN groups_claim;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
