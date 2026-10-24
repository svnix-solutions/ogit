package tokenutil

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig() config.Config {
	var cfg config.Config
	cfg.Session.SecretKey = strings.Repeat("s", 32)
	cfg.Session.ExpirationMinutes = 5
	cfg.Session.Issuer = "test"
	return cfg
}

func TestGetSessionIdFromToken(t *testing.T) {
	cfg := testConfig()

	t.Run("roundtrip", func(t *testing.T) {
		tok, err := CreateAccessToken("user-1", "session-1", cfg)
		require.NoError(t, err)
		id, err := GetSessionIdFromToken(tok, cfg)
		require.NoError(t, err)
		assert.Equal(t, "session-1", id)
	})

	t.Run("wrong_secret", func(t *testing.T) {
		tok, err := CreateAccessToken("user-1", "session-1", cfg)
		require.NoError(t, err)
		other := cfg
		other.Session.SecretKey = strings.Repeat("x", 32)
		_, err = GetSessionIdFromToken(tok, other)
		assert.Error(t, err)
	})

	sign := func(t *testing.T, method jwt.SigningMethod, key any, claims *JwtCustomClaims) string {
		t.Helper()
		s, err := jwt.NewWithClaims(method, claims).SignedString(key)
		require.NoError(t, err)
		return s
	}
	valid := func(session string) *JwtCustomClaims {
		return &JwtCustomClaims{SessionID: session, RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		}}
	}

	t.Run("rejects_alg_none", func(t *testing.T) {
		tok := sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid("session-1"))
		_, err := GetSessionIdFromToken(tok, cfg)
		assert.Error(t, err)
	})

	t.Run("rejects_other_hmac_alg", func(t *testing.T) {
		tok := sign(t, jwt.SigningMethodHS512, []byte(cfg.Session.SecretKey), valid("session-1"))
		_, err := GetSessionIdFromToken(tok, cfg)
		assert.Error(t, err)
	})

	t.Run("rejects_empty_session_id", func(t *testing.T) {
		tok := sign(t, jwt.SigningMethodHS256, []byte(cfg.Session.SecretKey), valid(""))
		_, err := GetSessionIdFromToken(tok, cfg)
		assert.Error(t, err)
	})

	t.Run("rejects_expired", func(t *testing.T) {
		claims := valid("session-1")
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
		tok := sign(t, jwt.SigningMethodHS256, []byte(cfg.Session.SecretKey), claims)
		_, err := GetSessionIdFromToken(tok, cfg)
		assert.Error(t, err)
	})
}
