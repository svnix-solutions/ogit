package migrations

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upActionArtifacts, downActionArtifacts)
}

// Actions artifacts (v4), stored in the repository's "artifacts" area, and
// a numeric api_id for runs: GitHub's REST API and the artifact clients
// parse run ids as numbers.
func upActionArtifacts(ctx context.Context, tx *sql.Tx) error {
	dialect, _ := ctx.Value("dbDialect").(string)
	ts, big := "TIMESTAMP", "INTEGER"
	switch dialect {
	case "sqlite":
	case "postgres":
		ts, big = "TIMESTAMPTZ", "BIGINT"
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	queries := []string{
		`ALTER TABLE action_run ADD COLUMN api_id ` + big + ` NOT NULL DEFAULT 0;`,
		`CREATE TABLE action_artifact (
			id TEXT PRIMARY KEY,
			api_id ` + big + ` NOT NULL UNIQUE,
			run_id TEXT NOT NULL,
			repository_id TEXT NOT NULL,
			name TEXT NOT NULL,
			size ` + big + ` NOT NULL DEFAULT 0,
			ready BOOLEAN NOT NULL DEFAULT FALSE,
			expires_at ` + ts + ` NOT NULL,
			created_at ` + ts + ` NOT NULL,
			UNIQUE(run_id, name)
		);`,
		`CREATE INDEX idx_action_artifact_repo ON action_artifact(repository_id);`,
		// Blocks of uploads in progress, so abandoned ones can be deleted.
		`CREATE TABLE action_artifact_block (
			artifact_id TEXT NOT NULL,
			block_id TEXT NOT NULL,
			PRIMARY KEY (artifact_id, block_id)
		);`,
		`CREATE INDEX idx_action_artifact_expires ON action_artifact(expires_at);`,
	}
	for _, q := range queries {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// Existing runs get an api_id too.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM action_run`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		q := `UPDATE action_run SET api_id = ? WHERE id = ?`
		if dialect == "postgres" {
			q = `UPDATE action_run SET api_id = $1 WHERE id = $2`
		}
		if _, err := tx.ExecContext(ctx, q, randomAPIID(), id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX idx_action_run_api_id ON action_run(api_id);`)
	return err
}

// randomAPIID is below 2^53, so JavaScript clients read it exactly.
func randomAPIID() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:])>>11) + 1
}

func downActionArtifacts(ctx context.Context, tx *sql.Tx) error {
	for _, q := range []string{
		`DROP TABLE IF EXISTS action_artifact_block;`,
		`DROP TABLE IF EXISTS action_artifact;`,
		`DROP INDEX IF EXISTS idx_action_run_api_id;`,
		`ALTER TABLE action_run DROP COLUMN api_id;`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}
