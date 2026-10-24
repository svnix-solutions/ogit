package websession

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecure(t *testing.T) {
	for _, tc := range []struct {
		publicURL string
		forwarded string
		want      bool
	}{
		{"", "", false},
		{"http://localhost:8080", "", false},
		{"https://git.example.com", "", true},
		{"", "https", true}, // a TLS-terminating proxy; spoofing it only makes the sender's own cookie stricter
	} {
		app := fiber.New()
		var got bool
		app.Get("/", func(c *fiber.Ctx) error { got = Secure(c, tc.publicURL); return nil })
		req := httptest.NewRequest("GET", "/", nil)
		if tc.forwarded != "" {
			req.Header.Set("X-Forwarded-Proto", tc.forwarded)
		}
		_, err := app.Test(req)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "%+v", tc)
	}
}
