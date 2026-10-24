//go:build integration

package server

import (
	"net/http"
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The whole flow over real SMTP: the server's own SMTPSender delivers to
// Mailpit, and the link is read back from there.
func TestEmailVerificationMailpit(t *testing.T) {
	mp := mailtest.StartMailpit(t)
	srv := startTestServer(t, requireVerification, func(c *config.Config) {
		c.SMTP.Host = mp.Host
		c.SMTP.Port = mp.SMTPPort
		c.SMTP.TLS = "none"
	})

	srv.register(t, "alice@example.com")
	msgs := mp.Messages(t, "alice@example.com")
	require.Len(t, msgs, 1)
	assert.Equal(t, "Verify your email address", msgs[0].Subject)
	m := verifyLink.FindStringSubmatch(msgs[0].Body)
	require.NotNil(t, m, "email contains a verification link: %q", msgs[0].Body)

	status, _ := srv.login(t, "alice@example.com")
	require.Equal(t, http.StatusForbidden, status)
	status, _, _ = srv.openLink(t, m[1])
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.login(t, "alice@example.com")
	assert.Equal(t, http.StatusOK, status)
}
