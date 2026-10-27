package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSSOLDAP, downSSOLDAP)
}

// LDAP / Active Directory providers: a third kind, with its settings as
// JSON. SQLite can't change a CHECK, so its table is rebuilt.
func upSSOLDAP(ctx context.Context, tx *sql.Tx) error {
	return ssoKinds(ctx, tx, "'oidc', 'github', 'ldap'", true)
}

func downSSOLDAP(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sso_provider WHERE kind = 'ldap';`); err != nil {
		return err
	}
	return ssoKinds(ctx, tx, "'oidc', 'github'", false)
}

// ssoKinds sets the allowed provider kinds, with or without ldap_config.
func ssoKinds(ctx context.Context, tx *sql.Tx, kinds string, ldap bool) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	var queries []string
	switch dialect {
	case "sqlite":
		ldapColumn, copyColumns := "", "id, slug, name, kind, issuer, client_id, client_secret, scopes, allowed_domains, allowed_orgs, auto_create, require_two_factor, enabled, groups_claim, admin_group, group_mappings, created_at, updated_at"
		if ldap {
			ldapColumn = "ldap_config TEXT NOT NULL DEFAULT '{}',"
		}
		queries = []string{
			`CREATE TABLE sso_provider_new (
				id TEXT PRIMARY KEY,
				slug TEXT NOT NULL UNIQUE,
				name TEXT NOT NULL,
				kind TEXT NOT NULL CHECK (kind IN (` + kinds + `)),
				issuer TEXT NOT NULL,
				client_id TEXT NOT NULL,
				client_secret TEXT NOT NULL DEFAULT '',
				scopes TEXT NOT NULL DEFAULT '',
				allowed_domains TEXT NOT NULL DEFAULT '',
				allowed_orgs TEXT NOT NULL DEFAULT '',
				auto_create BOOLEAN NOT NULL DEFAULT TRUE,
				require_two_factor BOOLEAN NOT NULL DEFAULT FALSE,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				groups_claim TEXT NOT NULL DEFAULT '',
				admin_group TEXT NOT NULL DEFAULT '',
				group_mappings TEXT NOT NULL DEFAULT '[]',
				` + ldapColumn + `
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL
			);`,
			`INSERT INTO sso_provider_new (` + copyColumns + `) SELECT ` + copyColumns + ` FROM sso_provider;`,
			`DROP TABLE sso_provider;`,
			`ALTER TABLE sso_provider_new RENAME TO sso_provider;`,
		}
	case "postgres":
		queries = []string{
			`ALTER TABLE sso_provider DROP CONSTRAINT sso_provider_kind_check;`,
			`ALTER TABLE sso_provider ADD CONSTRAINT sso_provider_kind_check CHECK (kind IN (` + kinds + `));`,
		}
		if ldap {
			queries = append(queries, `ALTER TABLE sso_provider ADD COLUMN ldap_config TEXT NOT NULL DEFAULT '{}';`)
		} else {
			queries = append(queries, `ALTER TABLE sso_provider DROP COLUMN ldap_config;`)
		}
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
