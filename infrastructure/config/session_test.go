package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureSessionSecret(t *testing.T) {
	t.Run("generates_random_secret_when_unset", func(t *testing.T) {
		var logs bytes.Buffer
		var a, b Config
		require.NoError(t, a.EnsureSessionSecret(zerolog.New(&logs)))
		require.NoError(t, b.EnsureSessionSecret(zerolog.Nop()))

		assert.GreaterOrEqual(t, len(a.Session.SecretKey), MinSessionSecretLength)
		assert.NotEqual(t, a.Session.SecretKey, b.Session.SecretKey)
		assert.Contains(t, logs.String(), "secret_generated")
		assert.NotContains(t, logs.String(), a.Session.SecretKey, "generated secret must not be logged")
	})

	t.Run("rejects_short_secret", func(t *testing.T) {
		var c Config
		c.Session.SecretKey = "supersecretkey"
		assert.Error(t, c.EnsureSessionSecret(zerolog.Nop()))
	})

	t.Run("keeps_valid_secret", func(t *testing.T) {
		var c Config
		c.Session.SecretKey = strings.Repeat("k", MinSessionSecretLength)
		require.NoError(t, c.EnsureSessionSecret(zerolog.Nop()))
		assert.Equal(t, strings.Repeat("k", MinSessionSecretLength), c.Session.SecretKey)
	})
}
