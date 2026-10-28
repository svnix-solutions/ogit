package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upWebhook, downWebhook)
}

// Webhooks and their deliveries (the queue and the delivery log).
func upWebhook(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE webhook (
			id TEXT PRIMARY KEY,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			secret TEXT NOT NULL DEFAULT '',
			events TEXT NOT NULL DEFAULT '["push"]',
			active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX idx_webhook_repository ON webhook(repository_id);

		CREATE TABLE webhook_delivery (
			id TEXT PRIMARY KEY,
			webhook_id TEXT NOT NULL REFERENCES webhook(id) ON DELETE CASCADE,
			event TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pending', 'delivered', 'failed')),
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TIMESTAMP NOT NULL,
			locked_until TIMESTAMP,
			response_status INTEGER NOT NULL DEFAULT 0,
			response_body TEXT NOT NULL DEFAULT '',
			duration_ms INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			delivered_at TIMESTAMP
		);
		CREATE INDEX idx_webhook_delivery_due ON webhook_delivery(status, next_attempt_at);
		CREATE INDEX idx_webhook_delivery_hook ON webhook_delivery(webhook_id, created_at);
		`
	case "postgres":
		query = `
		CREATE TABLE webhook (
			id UUID PRIMARY KEY,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			url TEXT NOT NULL,
			secret TEXT NOT NULL DEFAULT '',
			events TEXT NOT NULL DEFAULT '["push"]',
			active BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		CREATE INDEX idx_webhook_repository ON webhook(repository_id);

		CREATE TABLE webhook_delivery (
			id UUID PRIMARY KEY,
			webhook_id UUID NOT NULL REFERENCES webhook(id) ON DELETE CASCADE,
			event TEXT NOT NULL,
			action TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('pending', 'delivered', 'failed')),
			attempts INTEGER NOT NULL DEFAULT 0,
			next_attempt_at TIMESTAMPTZ NOT NULL,
			locked_until TIMESTAMPTZ,
			response_status INTEGER NOT NULL DEFAULT 0,
			response_body TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			delivered_at TIMESTAMPTZ
		);
		CREATE INDEX idx_webhook_delivery_due ON webhook_delivery(status, next_attempt_at);
		CREATE INDEX idx_webhook_delivery_hook ON webhook_delivery(webhook_id, created_at);
		`
	default:
		return fmt.Errorf("unsupported dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downWebhook(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS webhook_delivery; DROP TABLE IF EXISTS webhook;`)
	return err
}
