package server

import (
	"net/http"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
)

func TestRegistrationPolicy(t *testing.T) {
	register := func(srv *testServer, email, password string) (int, map[string]any) {
		return srv.post(t, "/api/v1/auth/register", "", map[string]any{"email": email, "username": "u", "password": password})
	}

	t.Run("weak_password_is_rejected", func(t *testing.T) {
		srv := startTestServer(t)
		status, body := register(srv, "a@example.com", "password123")
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "REGISTRATION_REJECTED", body["type"])
	})

	t.Run("disabled", func(t *testing.T) {
		srv := startTestServer(t, func(c *config.Config) { c.Registration.Enabled = false })
		status, body := register(srv, "a@example.com", "Correct-Horse-Battery-9")
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "REGISTRATION_DISABLED", body["type"])
	})

	t.Run("domain_whitelist", func(t *testing.T) {
		srv := startTestServer(t, func(c *config.Config) { c.Registration.DomainWhitelist = []string{"example.com"} })
		status, _ := register(srv, "a@evil.com", "Correct-Horse-Battery-9")
		assert.Equal(t, http.StatusBadRequest, status)
		status, body := register(srv, "a@example.com", "Correct-Horse-Battery-9")
		assert.Equal(t, http.StatusOK, status, "%v", body)
	})
}
