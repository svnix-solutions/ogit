package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upSSOSAML, downSSOSAML)
}

// SAML 2.0 providers: a fourth kind with its settings as JSON, the
// verified result a flow carries from the assertion consumer to the
// browser's next step, and the assertions used (no replays).
func upSSOSAML(ctx context.Context, tx *sql.Tx) error {
	if err := ssoKindsWith(ctx, tx, "'oidc', 'github', 'ldap', 'saml'", true); err != nil {
		return err
	}
	dialect, _ := ctx.Value("dbDialect").(string)
	ts := "TIMESTAMP"
	if dialect == "postgres" {
		ts = "TIMESTAMPTZ"
	}
	for _, q := range []string{
		`ALTER TABLE sso_flow ADD COLUMN result TEXT NOT NULL DEFAULT '';`,
		`CREATE TABLE sso_used_assertion (
			id TEXT PRIMARY KEY,
			expires_at ` + ts + ` NOT NULL
		);`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func downSSOSAML(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{`DROP TABLE IF EXISTS sso_used_assertion;`, `ALTER TABLE sso_flow DROP COLUMN result;`, `DELETE FROM sso_provider WHERE kind = 'saml';`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return ssoKindsWith(ctx, tx, "'oidc', 'github', 'ldap'", false)
}

// ssoKindsWith sets the allowed provider kinds (after the LDAP
// migration), with or without saml_config.
func ssoKindsWith(ctx context.Context, tx *sql.Tx, kinds string, saml bool) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	var queries []string
	switch dialect {
	case "sqlite":
		columns := "id, slug, name, kind, issuer, client_id, client_secret, scopes, allowed_domains, allowed_orgs, auto_create, require_two_factor, enabled, groups_claim, admin_group, group_mappings, ldap_config, created_at, updated_at"
		samlColumn := ""
		if saml {
			samlColumn = "saml_config TEXT NOT NULL DEFAULT '{}',"
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
				ldap_config TEXT NOT NULL DEFAULT '{}',
				` + samlColumn + `
				created_at TIMESTAMP NOT NULL,
				updated_at TIMESTAMP NOT NULL
			);`,
			`INSERT INTO sso_provider_new (` + columns + `) SELECT ` + columns + ` FROM sso_provider;`,
			`DROP TABLE sso_provider;`,
			`ALTER TABLE sso_provider_new RENAME TO sso_provider;`,
		}
	case "postgres":
		queries = []string{
			`ALTER TABLE sso_provider DROP CONSTRAINT sso_provider_kind_check;`,
			`ALTER TABLE sso_provider ADD CONSTRAINT sso_provider_kind_check CHECK (kind IN (` + kinds + `));`,
		}
		if saml {
			queries = append(queries, `ALTER TABLE sso_provider ADD COLUMN saml_config TEXT NOT NULL DEFAULT '{}';`)
		} else {
			queries = append(queries, `ALTER TABLE sso_provider DROP COLUMN saml_config;`)
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
