package mail

import (
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildMessage(t *testing.T) {
	from := &mail.Address{Name: "oGit", Address: "git@example.com"}
	to := &mail.Address{Address: "alice@example.com"}
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

	msg, err := buildMessage(from, to, "Verify your email address", "line one\nline two\n", now)
	require.NoError(t, err)
	s := string(msg)

	headers, body, ok := strings.Cut(s, "\r\n\r\n")
	require.True(t, ok, "headers and body are separated by a blank line")
	assert.Contains(t, headers, `From: "oGit" <git@example.com>`)
	assert.Contains(t, headers, "To: <alice@example.com>")
	assert.Contains(t, headers, "Subject: Verify your email address")
	assert.Contains(t, headers, "Content-Type: text/plain; charset=UTF-8")
	assert.Regexp(t, `Message-ID: <[0-9a-f]{24}@example\.com>`, headers)
	assert.Equal(t, "line one\r\nline two\r\n", body, "bare LF becomes CRLF")
	assert.NotContains(t, strings.ReplaceAll(s, "\r\n", ""), "\n", "no bare LF anywhere")

	_, err = buildMessage(from, to, "Hello\r\nBcc: victim@example.com", "x", now)
	assert.Error(t, err, "line breaks in the subject would inject headers")

	msg, err = buildMessage(from, to, "Vérifiez votre adresse", "x", now)
	require.NoError(t, err)
	assert.Contains(t, string(msg), "Subject: =?UTF-8?q?V=C3=A9rifiez_votre_adresse?=")
}

func TestSendRejectsBadAddressesBeforeConnecting(t *testing.T) {
	var cfg config.Config
	cfg.SMTP.Host = "smtp.invalid" // never reached
	cfg.SMTP.Port = 25
	cfg.SMTP.From = "git@example.com"
	s := NewSMTPSender(cfg)

	err := s.Send("alice@example.com\r\nBcc: victim@example.com", "subject", "body")
	assert.ErrorContains(t, err, "invalid recipient")

	cfg.SMTP.From = "not an address"
	assert.ErrorContains(t, NewSMTPSender(cfg).Send("alice@example.com", "s", "b"), "invalid smtp.from")
}
