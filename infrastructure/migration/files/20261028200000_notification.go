package migrations

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

func init() {
	goose.AddMigrationContext(upNotification, downNotification)
}

// Notifications: one row per user and thread (issue, pull request,
// release), updated by each new event; repository watch levels; threads a
// user unsubscribed from; and email preferences.
func upNotification(ctx context.Context, tx *sql.Tx) error {
	var query string
	dialect, _ := ctx.Value("dbDialect").(string)
	switch dialect {
	case "sqlite":
		query = `
		CREATE TABLE notification (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			thread_type TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			thread_number INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL,
			participating BOOLEAN NOT NULL DEFAULT TRUE,
			summary TEXT NOT NULL DEFAULT '',
			actor_id TEXT REFERENCES user(id) ON DELETE SET NULL,
			unread BOOLEAN NOT NULL DEFAULT TRUE,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, thread_type, thread_id)
		);
		CREATE INDEX idx_notification_inbox ON notification(user_id, unread, updated_at);
		CREATE TABLE repository_watch (
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			repository_id TEXT NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			level TEXT NOT NULL CHECK (level IN ('participating', 'all', 'releases', 'ignore')),
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, repository_id)
		);
		CREATE INDEX idx_repository_watch_repo ON repository_watch(repository_id, level);
		CREATE TABLE thread_mute (
			user_id TEXT NOT NULL REFERENCES user(id) ON DELETE CASCADE,
			thread_type TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (user_id, thread_type, thread_id)
		);
		CREATE TABLE notification_preference (
			user_id TEXT PRIMARY KEY REFERENCES user(id) ON DELETE CASCADE,
			email TEXT NOT NULL CHECK (email IN ('participating', 'all', 'none'))
		);
		`
	case "postgres":
		query = `
		CREATE TABLE notification (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			thread_type TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			thread_number INTEGER NOT NULL DEFAULT 0,
			title TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL,
			participating BOOLEAN NOT NULL DEFAULT TRUE,
			summary TEXT NOT NULL DEFAULT '',
			actor_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
			unread BOOLEAN NOT NULL DEFAULT TRUE,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(user_id, thread_type, thread_id)
		);
		CREATE INDEX idx_notification_inbox ON notification(user_id, unread, updated_at);
		CREATE TABLE repository_watch (
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			repository_id UUID NOT NULL REFERENCES repository(id) ON DELETE CASCADE,
			level TEXT NOT NULL CHECK (level IN ('participating', 'all', 'releases', 'ignore')),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, repository_id)
		);
		CREATE INDEX idx_repository_watch_repo ON repository_watch(repository_id, level);
		CREATE TABLE thread_mute (
			user_id UUID NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
			thread_type TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (user_id, thread_type, thread_id)
		);
		CREATE TABLE notification_preference (
			user_id UUID PRIMARY KEY REFERENCES "user"(id) ON DELETE CASCADE,
			email TEXT NOT NULL CHECK (email IN ('participating', 'all', 'none'))
		);
		`
	default:
		return fmt.Errorf("unsupported database dialect: %s", dialect)
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

func downNotification(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS notification_preference; DROP TABLE IF EXISTS thread_mute;
		DROP TABLE IF EXISTS repository_watch; DROP TABLE IF EXISTS notification;`)
	return err
}
