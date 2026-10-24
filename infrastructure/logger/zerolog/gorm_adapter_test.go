package zerolog

import (
	"bytes"
	"testing"

	z "github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Bound values (password hashes, token hashes, emails) must not reach the
// logs, including the error-level entry written when a query fails.
func TestGormLoggerOmitsBoundValues(t *testing.T) {
	var logs bytes.Buffer
	logger := z.New(&logs).Level(z.DebugLevel)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: NewGormLogger(logger)})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE secrets (id TEXT PRIMARY KEY, value TEXT)").Error)

	const secret = "s3cr3t-value-that-must-not-be-logged"
	require.NoError(t, db.Exec("INSERT INTO secrets (id, value) VALUES (?, ?)", "1", secret).Error)
	// Duplicate primary key: logged at error level with the SQL.
	require.Error(t, db.Exec("INSERT INTO secrets (id, value) VALUES (?, ?)", "1", secret).Error)

	out := logs.String()
	assert.Contains(t, out, "INSERT INTO secrets", "SQL is still logged")
	assert.Contains(t, out, `"level":"error"`)
	assert.NotContains(t, out, secret)
}
