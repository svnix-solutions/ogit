//go:build integration

package server

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// With OGIT_TEST_DB=postgres, every test server in this package uses its own
// database on one shared Postgres container instead of sqlite:
//
//	OGIT_TEST_DB=postgres go test -tags=integration ./interfaces/cli/server/
func init() {
	if os.Getenv("OGIT_TEST_DB") == "postgres" {
		testDatabases = append(testDatabases, usePostgres)
	}
}

var (
	pgOnce sync.Once
	pgDSN  string // DSN of the server's maintenance database
	pgErr  error
)

func usePostgres(t *testing.T, cfg *config.Config) {
	t.Helper()

	// The container is shared by all tests and removed by the testcontainers
	// reaper when the test binary exits.
	pgOnce.Do(func() {
		ctr, err := postgres.Run(context.Background(), "postgres:17-alpine",
			postgres.WithDatabase("ogit"), postgres.WithUsername("ogit"), postgres.WithPassword("ogit"),
			postgres.BasicWaitStrategies())
		if err != nil {
			pgErr = err
			return
		}
		pgDSN, pgErr = ctr.ConnectionString(context.Background(), "sslmode=disable")
	})
	require.NoError(t, pgErr)

	admin, err := sql.Open("pgx", pgDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close() })

	name := "t_" + strings.ToLower(strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	if len(name) > 50 {
		name = name[:50]
	}
	name = fmt.Sprintf("%s_%d", name, len(t.Name())) // distinct for long, similar names
	_, err = admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`) })

	cfg.Database.Dialect = "postgres"
	cfg.Database.DSN = strings.Replace(pgDSN, "/ogit?", "/"+name+"?", 1)
}
