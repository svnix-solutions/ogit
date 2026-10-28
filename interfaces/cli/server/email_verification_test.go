package server

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testPassword = "Correct-Horse-Battery-9"

var verifyLink = regexp.MustCompile(`https://git\.example\.com/api/v1/auth/verify\?token=([0-9a-f]{64})`)

func requireVerification(c *config.Config) {
	c.Registration.RequireEmailVerification = true
	c.Registration.VerificationTTLHours = 24
	c.Registration.VerificationURLTemplate = config.DefaultVerificationURLTemplate
	c.Server.PublicURL = "https://git.example.com/"
	c.SMTP.Host = "smtp.invalid" // never contacted: the recorder replaces the sender
	c.SMTP.From = "oGit <git@example.com>"
	c.SMTP.TLS = "starttls"
}

// startVerifyingServer starts a server that requires email verification and
// records outgoing mail.
func startVerifyingServer(t *testing.T, opts ...func(*config.Config)) (*testServer, *mailtest.Recorder) {
	t.Helper()
	srv := startTestServer(t, append([]func(*config.Config){requireVerification}, opts...)...)
	rec := &mailtest.Recorder{}
	srv.deps.AuthApp.Mailer = rec
	return srv, rec
}

func (s *testServer) register(t *testing.T, email string) {
	t.Helper()
	status, body := s.post(t, "/api/v1/auth/register", "", map[string]any{"email": email, "username": strings.Split(email, "@")[0], "password": testPassword})
	require.Equal(t, http.StatusOK, status, "register: %v", body)
}

func (s *testServer) login(t *testing.T, email string) (int, map[string]any) {
	t.Helper()
	return s.post(t, "/api/v1/auth/login", "", map[string]any{"email": email, "password": testPassword})
}

// openLink GETs a verification link (rewritten to the test server) and
// returns the status and page.
func (s *testServer) openLink(t *testing.T, token string) (int, string, http.Header) {
	t.Helper()
	resp, err := http.Get(s.url + "/api/v1/auth/verify?token=" + token)
	require.NoError(t, err)
	defer resp.Body.Close()
	page, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(page), resp.Header
}

func lastToken(t *testing.T, rec *mailtest.Recorder, email string) string {
	t.Helper()
	msgs := rec.Messages(email)
	require.NotEmpty(t, msgs, "an email was sent to %s", email)
	m := verifyLink.FindStringSubmatch(msgs[len(msgs)-1].Body)
	require.NotNil(t, m, "email contains a verification link: %q", msgs[len(msgs)-1].Body)
	return m[1]
}

// ageVerification makes the pending verification look older, to get past the
// resend rate limit or the expiry.
func (s *testServer) ageVerification(t *testing.T, email string, created, expires time.Time) {
	t.Helper()
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("email = ?", email).First(&u).Error)
	require.NoError(t, s.deps.Database.Db.Model(&domain.EmailVerification{}).Where("user_id = ?", u.Id).
		Updates(map[string]any{"created_at": created, "expires_at": expires}).Error)
}

func TestEmailVerification(t *testing.T) {
	srv, rec := startVerifyingServer(t)

	t.Run("full_flow", func(t *testing.T) {
		srv.register(t, "alice@example.com")
		msgs := rec.Messages("alice@example.com")
		require.Len(t, msgs, 1)
		assert.Equal(t, "Verify your email address", msgs[0].Subject)
		assert.Contains(t, msgs[0].Body, "within 24 hours")
		assert.Contains(t, msgs[0].Body, "Hello alice")

		status, body := srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusForbidden, status)
		assert.Equal(t, "EMAIL_NOT_VERIFIED", body["type"])

		status, _ = srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "alice@example.com", "password": "Wrong-Password-1"})
		assert.Equal(t, http.StatusUnauthorized, status, "a wrong password does not reveal verification status")

		token := lastToken(t, rec, "alice@example.com")
		status, page, header := srv.openLink(t, token)
		assert.Equal(t, http.StatusOK, status)
		assert.Contains(t, page, "Email verified")
		assert.Equal(t, "no-referrer", header.Get("Referrer-Policy"))
		assert.Contains(t, header.Get("Content-Type"), "text/html")

		status, _ = srv.login(t, "alice@example.com")
		assert.Equal(t, http.StatusOK, status)

		status, page, _ = srv.openLink(t, token)
		assert.Equal(t, http.StatusBadRequest, status, "links are single use")
		assert.Contains(t, page, "Link not valid")
	})

	t.Run("api_endpoint", func(t *testing.T) {
		srv.register(t, "bob@example.com")
		status, _ := srv.post(t, "/api/v1/auth/verify", "", map[string]any{"token": strings.Repeat("0", 64)})
		assert.Equal(t, http.StatusBadRequest, status)
		status, body := srv.post(t, "/api/v1/auth/verify", "", map[string]any{"token": lastToken(t, rec, "bob@example.com")})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.login(t, "bob@example.com")
		assert.Equal(t, http.StatusOK, status)
	})

	t.Run("expired_link", func(t *testing.T) {
		srv.register(t, "carol@example.com")
		srv.ageVerification(t, "carol@example.com", time.Now().Add(-25*time.Hour), time.Now().Add(-time.Hour))
		status, _, _ := srv.openLink(t, lastToken(t, rec, "carol@example.com"))
		assert.Equal(t, http.StatusBadRequest, status)
		status, _ = srv.login(t, "carol@example.com")
		assert.Equal(t, http.StatusForbidden, status)
	})

	t.Run("resend", func(t *testing.T) {
		resend := func(email string) {
			t.Helper()
			status, body := srv.post(t, "/api/v1/auth/verify/resend", "", map[string]any{"email": email})
			require.Equal(t, http.StatusOK, status, "resend always answers 200: %v", body)
			assert.Contains(t, body["message"], "If an unverified account exists")
		}

		srv.register(t, "dave@example.com")
		first := lastToken(t, rec, "dave@example.com")

		resend("dave@example.com")
		assert.Len(t, rec.Messages("dave@example.com"), 1, "rate limited right after registration")

		srv.ageVerification(t, "dave@example.com", time.Now().Add(-2*time.Minute), time.Now().Add(time.Hour))
		resend("dave@example.com")
		require.Len(t, rec.Messages("dave@example.com"), 2)
		second := lastToken(t, rec, "dave@example.com")
		assert.NotEqual(t, first, second)

		status, _, _ := srv.openLink(t, first)
		assert.Equal(t, http.StatusBadRequest, status, "a new link invalidates the old one")
		status, _, _ = srv.openLink(t, second)
		assert.Equal(t, http.StatusOK, status)

		resend("nobody@example.com")
		assert.Empty(t, rec.Messages("nobody@example.com"), "unknown address: same answer, no email")
		resend("alice@example.com")
		assert.Len(t, rec.Messages("alice@example.com"), 1, "verified account: same answer, no email")
	})

	t.Run("send_failure_keeps_the_account", func(t *testing.T) {
		rec.Fail(true)
		srv.register(t, "erin@example.com")
		rec.Fail(false)
		assert.Empty(t, rec.Messages("erin@example.com"))
		status, _ := srv.login(t, "erin@example.com")
		assert.Equal(t, http.StatusForbidden, status)

		srv.ageVerification(t, "erin@example.com", time.Now().Add(-2*time.Minute), time.Now().Add(time.Hour))
		status, _ = srv.post(t, "/api/v1/auth/verify/resend", "", map[string]any{"email": "erin@example.com"})
		require.Equal(t, http.StatusOK, status)
		status, _, _ = srv.openLink(t, lastToken(t, rec, "erin@example.com"))
		require.Equal(t, http.StatusOK, status)
		status, _ = srv.login(t, "erin@example.com")
		assert.Equal(t, http.StatusOK, status)
	})
}

func TestEmailVerificationDisabled(t *testing.T) {
	srv := startTestServer(t)
	rec := &mailtest.Recorder{}
	srv.deps.AuthApp.Mailer = rec

	srv.register(t, "alice@example.com")
	assert.Empty(t, rec.Messages("alice@example.com"), "no email when verification is off")
	status, _ := srv.login(t, "alice@example.com")
	assert.Equal(t, http.StatusOK, status)

	var u domain.User
	require.NoError(t, srv.deps.Database.Db.Where("email = ?", "alice@example.com").First(&u).Error)
	assert.NotNil(t, u.EmailVerifiedAt, "accounts created while verification is off are verified")

	// Turning verification on later does not lock this account out.
	later, _ := startVerifyingServer(t, func(c *config.Config) { c.Database = srv.deps.Config.Database })
	status, _ = later.login(t, "alice@example.com")
	assert.Equal(t, http.StatusOK, status)
}

func TestSetupDepsRequiresSMTPForVerification(t *testing.T) {
	dir := t.TempDir()
	var cfg config.Config
	cfg.Logger.Level = "error"
	cfg.Database.Dialect = "sqlite"
	cfg.Database.DSN = dir + "/ogit.sqlite"
	cfg.Storage.Type = "local"
	cfg.Storage.Local.Path = dir + "/repositories"
	cfg.Session.SecretKey = "test-secret-at-least-32-bytes-long"
	cfg.Registration.RequireEmailVerification = true

	_, err := setupDeps(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "smtp.host, smtp.from, http.public_url")
}
