package admin

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/database"
	"github.com/labbs/ogit/infrastructure/migration"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromoteDemote(t *testing.T) {
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = filepath.Join(t.TempDir(), "ogit.sqlite")

	db, err := database.Configure(cfg, zerolog.Nop())
	require.NoError(t, err)
	require.NoError(t, migration.RunMigration(zerolog.Nop(), db.Db))
	require.NoError(t, db.Db.Create(&domain.User{Id: "u1", Username: "alice", Email: "alice@example.com", Password: "x", Active: true}).Error)
	role := func() domain.Role {
		var u domain.User
		require.NoError(t, db.Db.Where("id = ?", "u1").First(&u).Error)
		return u.Role
	}

	var out bytes.Buffer
	require.NoError(t, run(cfg, "alice@example.com", "", domain.RoleAdmin, &out))
	assert.Equal(t, domain.RoleAdmin, role())
	assert.Equal(t, "alice (alice@example.com) is now admin\n", out.String())

	require.NoError(t, run(cfg, "", "alice", domain.RoleUser, &out), "by username, and the CLI may remove the last admin")
	assert.Equal(t, domain.RoleUser, role())

	assert.Error(t, run(cfg, "", "", domain.RoleAdmin, &out), "needs --email or --username")
	assert.Error(t, run(cfg, "alice@example.com", "alice", domain.RoleAdmin, &out), "not both")
	assert.ErrorContains(t, run(cfg, "nobody@example.com", "", domain.RoleAdmin, &out), "user not found")

	// reset-2fa
	require.NoError(t, db.Db.Create(&domain.UserTOTP{UserId: "u1", Secret: "sealed"}).Error)
	require.NoError(t, db.Db.Create(&domain.RecoveryCode{Id: "c1", UserId: "u1", CodeHash: "h"}).Error)
	out.Reset()
	require.NoError(t, resetTwoFactor(cfg, "", "alice", &out))
	assert.Equal(t, "two-factor authentication is off for alice (alice@example.com)\n", out.String())
	var n int64
	require.NoError(t, db.Db.Model(&domain.UserTOTP{}).Count(&n).Error)
	assert.Zero(t, n)
	require.NoError(t, db.Db.Model(&domain.RecoveryCode{}).Count(&n).Error)
	assert.Zero(t, n)
	assert.ErrorContains(t, resetTwoFactor(cfg, "", "nobody", &out), "user not found")
}
