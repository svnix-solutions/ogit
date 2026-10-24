//go:build integration

package migration

import (
	"context"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	gpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The namespaces migration on Postgres, with data: ids are UUIDs there, so
// owner_id needs casts that SQLite does not exercise.
func TestPostgresMigrationNamespaces(t *testing.T) {
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("ogit"), postgres.WithUsername("ogit"), postgres.WithPassword("ogit"),
		postgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	db, err := gorm.Open(gpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(migrationFiles)
	require.NoError(t, goose.SetDialect("postgres"))
	gctx := context.WithValue(ctx, "dbDialect", "postgres")
	require.NoError(t, goose.UpToContext(gctx, sqlDB, "files", beforeNamespaces))

	const (
		alice = "11111111-1111-4111-8111-111111111111"
		bob   = "22222222-2222-4222-8222-222222222222"
		r1    = "33333333-3333-4333-8333-333333333333"
	)
	for _, q := range []string{
		`INSERT INTO "user" (id, username, email, password) VALUES ('` + alice + `', 'alice', 'a@example.com', 'x'), ('` + bob + `', 'bob', 'b@example.com', 'x')`,
		`INSERT INTO repository (id, name, slug, storage_key) VALUES ('` + r1 + `', 'Website', 'website', 'website')`,
		`INSERT INTO repository_member (id, repository_id, member_type, user_id, access_type, created_at, updated_at) VALUES
			(gen_random_uuid(), '` + r1 + `', 'user', '` + bob + `', 'owner', '2026-02-01', '2026-02-01'),
			(gen_random_uuid(), '` + r1 + `', 'user', '` + alice + `', 'owner', '2026-01-01', '2026-01-01')`,
		`INSERT INTO repository_redirect (slug, repository_id) VALUES ('old', '` + r1 + `')`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}

	require.NoError(t, goose.UpToContext(gctx, sqlDB, "files", namespaces))
	var row struct{ Namespace, OwnerType, OwnerId string }
	require.NoError(t, db.Raw(`SELECT namespace, owner_type, owner_id FROM repository WHERE id = ?`, r1).Scan(&row).Error)
	assert.Equal(t, "alice", row.Namespace, "the oldest owner")
	assert.Equal(t, "user", row.OwnerType)
	assert.Equal(t, alice, row.OwnerId)

	insert := func(ns string) error {
		return db.Exec(`INSERT INTO repository (id, name, namespace, owner_type, owner_id, slug, storage_key) VALUES (gen_random_uuid(), 'Website', ?, 'user', ?, 'website', gen_random_uuid()::text)`, ns, bob).Error
	}
	require.NoError(t, insert("bob"), "same slug and name in another namespace")
	assert.Error(t, insert("bob"), "but not twice in one")
	assert.Error(t, db.Exec(`UPDATE repository SET owner_type = 'team' WHERE id = ?`, r1).Error, "owner_type is checked")

	require.NoError(t, db.Exec(`DELETE FROM repository WHERE namespace = 'bob'`).Error)
	require.NoError(t, goose.DownToContext(gctx, sqlDB, "files", beforeNamespaces))
	require.NoError(t, goose.UpToContext(gctx, sqlDB, "files", namespaces))
}
