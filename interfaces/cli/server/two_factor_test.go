package server

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/totp"
	"github.com/labbs/ogit/infrastructure/mail/mailtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// code is the TOTP code of secret, offset steps from now (-1, 0 or 1 are
// accepted; each step only once).
func code(t *testing.T, secret string, offset int64) string {
	t.Helper()
	c, err := totp.Code(secret, totp.Step(time.Now())+offset)
	require.NoError(t, err)
	return c
}

// expireSudo ends the sudo window of every session of username.
func (s *testServer) expireSudo(t *testing.T, username string) {
	t.Helper()
	var u domain.User
	require.NoError(t, s.deps.Database.Db.Where("username = ?", username).First(&u).Error)
	require.NoError(t, s.deps.Database.Db.Model(&domain.Session{}).Where("user_id = ?", u.Id).Update("sudo_until", time.Now().Add(-time.Minute)).Error)
}

// enableTOTP turns on 2FA for as and returns the secret and recovery codes.
func (s *testServer) enableTOTP(t *testing.T, as user) (string, []string) {
	t.Helper()
	status, setup := s.post(t, "/api/v1/user/2fa/totp", as.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", setup)
	secret := setup["secret"].(string)
	status, body := s.post(t, "/api/v1/user/2fa/totp/confirm", as.session, map[string]any{"code": code(t, secret, 0)})
	require.Equal(t, http.StatusOK, status, "%v", body)
	var codes []string
	for _, c := range body["recovery_codes"].([]any) {
		codes = append(codes, c.(string))
	}
	return secret, codes
}

func TestTwoFactor(t *testing.T) {
	srv := startTestServer(t)
	alice := srv.newUser(t, "alice")
	mails := &mailtest.Recorder{}
	srv.deps.TwoFactorApp.Mailer = mails
	srv.deps.TwoFactorApp.Config.SMTP.Host = "smtp.test"
	srv.createRepo(t, alice.session, "App", true)

	var secret string
	var recovery []string
	t.Run("setup", func(t *testing.T) {
		status, body := srv.get(t, "/api/v1/user/2fa", alice.session)
		require.Equal(t, http.StatusOK, status)
		assert.Equal(t, false, body["enabled"])
		assert.Equal(t, true, body["totp_available"])

		status, body = srv.post(t, "/api/v1/user/2fa/totp/confirm", alice.session, map[string]any{"code": "123456"})
		assert.Equal(t, http.StatusConflict, status, "no setup started: %v", body)

		status, setup := srv.post(t, "/api/v1/user/2fa/totp", alice.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", setup)
		secret = setup["secret"].(string)
		assert.Regexp(t, `^otpauth://totp/oGit:alice\?`, setup["uri"])
		qr := setup["qr"].([]any)
		assert.Greater(t, len(qr), 20)
		assert.Regexp(t, `^[01]+$`, qr[0])
		var stored string
		require.NoError(t, srv.deps.Database.Db.Table("user_totp").Select("secret").Scan(&stored).Error)
		assert.NotContains(t, stored, secret, "encrypted at rest")

		status, body = srv.post(t, "/api/v1/user/2fa/totp/confirm", alice.session, map[string]any{"code": "000000"})
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
		_, profile := srv.get(t, "/api/v1/user/profile", alice.session)
		assert.Equal(t, false, profile["two_factor_enabled"], "not on before a valid code")

		status, body = srv.post(t, "/api/v1/user/2fa/totp/confirm", alice.session, map[string]any{"code": code(t, secret, 0)})
		require.Equal(t, http.StatusOK, status, "%v", body)
		for _, c := range body["recovery_codes"].([]any) {
			recovery = append(recovery, c.(string))
		}
		require.Len(t, recovery, 10)
		assert.Regexp(t, `^[a-z2-9]{5}-[a-z2-9]{5}$`, recovery[0])
		_, profile = srv.get(t, "/api/v1/user/profile", alice.session)
		assert.Equal(t, true, profile["two_factor_enabled"])
		_, body = srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.EqualValues(t, 10, body["recovery_codes_left"])
		require.Len(t, mails.Messages("alice@example.com"), 1)
		assert.Equal(t, "Two-factor authentication was turned on", mails.Messages("alice@example.com")[0].Subject)

		status, _ = srv.post(t, "/api/v1/user/2fa/totp", alice.session, nil)
		assert.Equal(t, http.StatusConflict, status, "already on")
	})

	login := func(t *testing.T) string {
		t.Helper()
		status, body := srv.login(t, "alice@example.com")
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["two_factor_required"])
		assert.Nil(t, body["token"], "no session before the second factor")
		return body["challenge"].(string)
	}
	second := func(t *testing.T, challenge, c string) (int, map[string]any) {
		t.Helper()
		return srv.post(t, "/api/v1/auth/login/2fa", "", map[string]any{"challenge": challenge, "code": c})
	}

	t.Run("login", func(t *testing.T) {
		ch := login(t)
		status, body := second(t, ch, "000000")
		assert.Equal(t, http.StatusUnauthorized, status)
		assert.Equal(t, "INVALID_CODE", body["type"])
		status, body = second(t, ch, code(t, secret, 0))
		assert.Equal(t, http.StatusUnauthorized, status, "the step used to confirm can't be replayed: %v", body)
		status, body = second(t, ch, code(t, secret, 1))
		require.Equal(t, http.StatusOK, status, "%v", body)
		token := body["token"].(string)
		status, _ = srv.get(t, "/api/v1/user/profile", token)
		assert.Equal(t, http.StatusOK, status, "a working session")
		status, _ = second(t, ch, code(t, secret, 1))
		assert.Equal(t, http.StatusUnauthorized, status, "a challenge is used once")

		ch = login(t)
		status, body = second(t, ch, strings.ToUpper(strings.ReplaceAll(recovery[0], "-", "")))
		require.Equal(t, http.StatusOK, status, "a recovery code, any case, with or without the dash: %v", body)
		ch = login(t)
		status, _ = second(t, ch, recovery[0])
		assert.Equal(t, http.StatusUnauthorized, status, "recovery codes are single-use")
		_, st := srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.EqualValues(t, 9, st["recovery_codes_left"])
	})

	t.Run("challenge_limits", func(t *testing.T) {
		ch := login(t)
		for range 5 {
			status, _ := second(t, ch, "000000")
			require.Equal(t, http.StatusUnauthorized, status)
		}
		status, body := second(t, ch, recovery[1])
		assert.Equal(t, "CHALLENGE_EXPIRED", body["type"], "5 attempts: %d %v", status, body)

		ch = login(t)
		require.NoError(t, srv.deps.Database.Db.Exec(`UPDATE login_challenge SET expires_at = ?`, time.Now().Add(-time.Second)).Error)
		_, body = second(t, ch, recovery[1])
		assert.Equal(t, "CHALLENGE_EXPIRED", body["type"])

		_, body = second(t, "made-up", recovery[1])
		assert.Equal(t, "CHALLENGE_EXPIRED", body["type"])
	})

	t.Run("web_session", func(t *testing.T) {
		status, body, h := srv.do(t, http.MethodPost, "/api/v1/auth/session", nil, map[string]any{"email": "alice@example.com", "password": testPassword})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Equal(t, true, body["two_factor_required"])
		assert.Empty(t, h.Values("Set-Cookie"), "no cookie before the second factor")
		status, body, h = srv.do(t, http.MethodPost, "/api/v1/auth/session/2fa", nil, map[string]any{"challenge": body["challenge"], "code": recovery[2]})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Contains(t, strings.Join(h.Values("Set-Cookie"), "\n"), "ogit_session=")
	})

	t.Run("sudo", func(t *testing.T) {
		srv.expireSudo(t, "alice")
		key := newSSHKey(t, "alice@x")
		for _, r := range []struct {
			path string
			body map[string]any
		}{
			{"/api/v1/user/token", map[string]any{"name": "late", "scopes": []string{"read:repository"}}},
			{"/api/v1/user/ssh-keys", map[string]any{"key": key.authorized}},
			{repoAPI("alice/app") + "/deploy-keys", map[string]any{"name": "ci", "key": key.authorized}},
			{repoAPI("alice/app") + "/tokens", map[string]any{"name": "ci", "scopes": []string{"read:repository"}}},
			{"/api/v1/user/2fa/recovery-codes", nil},
		} {
			status, body := srv.post(t, r.path, alice.session, r.body)
			assert.Equal(t, http.StatusForbidden, status, "%s: %v", r.path, body)
			assert.Equal(t, "SUDO_REQUIRED", body["type"], r.path)
		}
		status, body := srv.request(t, http.MethodDelete, "/api/v1/user/2fa", alice.session, nil)
		assert.Equal(t, http.StatusForbidden, status, "%v", body)

		status, body = srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"password": testPassword})
		assert.Equal(t, http.StatusUnauthorized, status, "with 2FA, the password is not enough: %v", body)
		status, body = srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"code": recovery[3]})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotEmpty(t, body["until"])
		status, body = srv.post(t, "/api/v1/user/token", alice.session, map[string]any{"name": "after-sudo", "scopes": []string{"read:repository"}})
		assert.Equal(t, http.StatusOK, status, "%v", body)
	})

	t.Run("tokens_keep_working", func(t *testing.T) {
		_, err := clone(t, srv.url+"/alice/app.git", alice.git)
		assert.NoError(t, err, "git with a personal access token needs no code")
	})

	t.Run("new_recovery_codes", func(t *testing.T) {
		status, body := srv.post(t, "/api/v1/user/2fa/recovery-codes", alice.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		fresh := body["recovery_codes"].([]any)
		require.Len(t, fresh, 10)
		_, body = second(t, login(t), recovery[4])
		assert.Equal(t, "INVALID_CODE", body["type"], "the old codes stop working")
		status, _ = second(t, login(t), fresh[0].(string))
		assert.Equal(t, http.StatusOK, status)
		recovery = nil
		for _, c := range fresh[1:] {
			recovery = append(recovery, c.(string))
		}
	})

	t.Run("disable", func(t *testing.T) {
		status, body := srv.request(t, http.MethodDelete, "/api/v1/user/2fa", alice.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, body = srv.login(t, "alice@example.com")
		require.Equal(t, http.StatusOK, status)
		assert.NotEmpty(t, body["token"], "the password alone again")
		subjects := []string{}
		for _, m := range mails.Messages("alice@example.com") {
			subjects = append(subjects, m.Subject)
		}
		assert.Equal(t, []string{"Two-factor authentication was turned on", "New recovery codes were generated", "Two-factor authentication was turned off"}, subjects)

		srv.expireSudo(t, "alice")
		status, body = srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"password": "wrong-password"})
		assert.Equal(t, http.StatusBadRequest, status, "%v", body)
		status, body = srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"password": testPassword})
		assert.Equal(t, http.StatusOK, status, "without 2FA, the password confirms: %v", body)
	})
}

func TestTwoFactorAdminReset(t *testing.T) {
	srv := startTestServer(t)
	admin := srv.newUser(t, "root")
	rootSecret, _ := srv.enableTOTP(t, admin)
	srv.promote(t, "root")
	bob := srv.newUser(t, "bob")
	srv.enableTOTP(t, bob)

	_, list := srv.get(t, "/api/v1/admin/users?q=bob", admin.session)
	require.Len(t, list["users"], 1)
	assert.Equal(t, true, list["users"].([]any)[0].(map[string]any)["two_factor_enabled"])

	status, body := srv.post(t, "/api/v1/admin/users/bob/reset-2fa", bob.session, nil)
	assert.Equal(t, http.StatusForbidden, status, "admins only: %v", body)
	srv.expireSudo(t, "root")
	status, body = srv.post(t, "/api/v1/admin/users/bob/reset-2fa", admin.session, nil)
	assert.Equal(t, "SUDO_REQUIRED", body["type"], "%d", status)
	status, _ = srv.post(t, "/api/v1/auth/sudo", admin.session, map[string]any{"code": code(t, rootSecret, 1)})
	require.Equal(t, http.StatusOK, status)
	status, body = srv.post(t, "/api/v1/admin/users/bob/reset-2fa", admin.session, nil)
	require.Equal(t, http.StatusOK, status, "%v", body)

	status, body = srv.login(t, "bob@example.com")
	require.Equal(t, http.StatusOK, status)
	assert.NotEmpty(t, body["token"], "bob signs in with the password and can set it up again")
	var n int64
	var bobRow domain.User
	require.NoError(t, srv.deps.Database.Db.Where("username = ?", "bob").First(&bobRow).Error)
	require.NoError(t, srv.deps.Database.Db.Model(&domain.RecoveryCode{}).Where("user_id = ?", bobRow.Id).Count(&n).Error)
	assert.Zero(t, n, "bob's recovery codes are gone too")
}

// Wrong codes are failed logins: the account's limit applies (only that
// limit is on here, so a 429 can't come from the API budgets).
func TestTwoFactorCodesAreRateLimited(t *testing.T) {
	srv := startTestServer(t, func(c *config.Config) {
		c.RateLimit.Enabled = true
		c.RateLimit.LoginAccount = "3/15m"
		c.RateLimit.LoginIP, c.RateLimit.RegisterIP, c.RateLimit.APIUser, c.RateLimit.APIAnonymous = "0", "0", "0", "0"
		c.RateLimit.GitUser, c.RateLimit.GitAnonymous = "0", "0"
	})
	carol := srv.newUser(t, "carol")
	srv.enableTOTP(t, carol)
	_, body := srv.login(t, "carol@example.com")
	ch := body["challenge"].(string)
	statuses := []int{}
	for range 4 {
		status, _ := srv.post(t, "/api/v1/auth/login/2fa", "", map[string]any{"challenge": ch, "code": "000000"})
		statuses = append(statuses, status)
	}
	assert.Equal(t, []int{401, 401, 401, 429}, statuses)
}
