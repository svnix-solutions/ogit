package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Scanners must accept every representation the drivers return: sqlite
// TEXT (string), postgres JSONB ([]byte) and NULL. A panic here deadlocks
// the database connection.
func TestJSONBScan(t *testing.T) {
	for name, in := range map[string]any{"string": `{"theme":"dark"}`, "bytes": []byte(`{"theme":"dark"}`)} {
		t.Run(name, func(t *testing.T) {
			var j JSONB
			require.NoError(t, j.Scan(in))
			assert.Equal(t, JSONB{"theme": "dark"}, j)
		})
	}

	t.Run("null", func(t *testing.T) {
		j := JSONB{"stale": true}
		require.NoError(t, j.Scan(nil))
		assert.Nil(t, j)
	})

	t.Run("unsupported_type_is_an_error_not_a_panic", func(t *testing.T) {
		var j JSONB
		assert.NotPanics(t, func() { assert.Error(t, j.Scan(42)) })
	})
}

func TestTokenScopesScan(t *testing.T) {
	for name, in := range map[string]any{"string": `["read:repository"]`, "bytes": []byte(`["read:repository"]`)} {
		t.Run(name, func(t *testing.T) {
			var s TokenScopes
			require.NoError(t, s.Scan(in))
			assert.Equal(t, TokenScopes{ScopeReadRepository}, s)
		})
	}

	t.Run("unsupported_type_is_an_error_not_a_panic", func(t *testing.T) {
		var s TokenScopes
		assert.NotPanics(t, func() { assert.Error(t, s.Scan(42)) })
	})
}
