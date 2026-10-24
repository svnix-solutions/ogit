package server

import (
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Login must not reveal whether an email is registered: an unknown email and
// a wrong password get the same answer and take about as long.
func TestLoginDoesNotRevealAccounts(t *testing.T) {
	srv := startTestServer(t)
	srv.register(t, "alice@example.com")

	login := func(email, password string) (int, map[string]any) {
		return srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": email, "password": password})
	}

	t.Run("same_answer", func(t *testing.T) {
		unknownStatus, unknownBody := login("nobody@example.com", testPassword)
		wrongStatus, wrongBody := login("alice@example.com", "Wrong-Password-1")
		assert.Equal(t, http.StatusUnauthorized, unknownStatus)
		assert.Equal(t, unknownStatus, wrongStatus)
		assert.Equal(t, unknownBody, wrongBody)
		assert.Equal(t, "INVALID_CREDENTIALS", wrongBody["type"])
	})

	t.Run("same_timing", func(t *testing.T) {
		median := func(email, password string) time.Duration {
			var d []time.Duration
			for i := 0; i < 7; i++ {
				start := time.Now()
				login(email, password)
				d = append(d, time.Since(start))
			}
			sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
			return d[len(d)/2]
		}
		wrong := median("alice@example.com", "Wrong-Password-1")
		unknown := median("nobody@example.com", "Wrong-Password-1")
		// Without the dummy comparison an unknown email answers in ~1ms
		// against ~50ms+ for a bcrypt check.
		assert.Greater(t, float64(unknown), 0.5*float64(wrong), "unknown %v vs wrong password %v", unknown, wrong)
	})

	t.Run("disabled_account", func(t *testing.T) {
		require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("email = ?", "alice@example.com").Update("active", false).Error)

		status, body := login("alice@example.com", "Wrong-Password-1")
		assert.Equal(t, http.StatusUnauthorized, status, "a wrong password does not reveal the account is disabled")
		assert.Equal(t, "INVALID_CREDENTIALS", body["type"])

		status, body = login("alice@example.com", testPassword)
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "ACCOUNT_DISABLED", body["type"])
	})
}
