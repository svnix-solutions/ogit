//go:build integration

package mail_test

import (
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSMTPSenderMailpit(t *testing.T) {
	mp := mailtest.StartMailpit(t)

	var cfg config.Config
	cfg.SMTP.Host = mp.Host
	cfg.SMTP.Port = mp.SMTPPort
	cfg.SMTP.From = "oGit <git@example.com>"
	cfg.SMTP.TLS = "none"

	require.NoError(t, mail.NewSMTPSender(cfg).Send("alice@example.com", "Vérification", "Hello alice,\nline two\n"))

	msgs := mp.Messages(t, "alice@example.com")
	require.Len(t, msgs, 1)
	assert.Equal(t, "Vérification", msgs[0].Subject, "non-ASCII subject survives encoding")
	assert.Contains(t, msgs[0].Body, "Hello alice,")
	assert.Contains(t, msgs[0].Body, "line two")
}
