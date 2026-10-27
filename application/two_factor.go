package application

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/labbs/ogit/infrastructure/helpers/totp"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

// Two-factor authentication rules:
//   - 2FA is on while a user has a confirmed authenticator app (TOTP) or a
//     security key or passkey (webauthn.go). TOTP setup stores a new secret
//     (encrypted with security.encryption_key) and the first valid code
//     confirms it. The first second factor comes with 10 recovery codes,
//     shown once (only their SHA-256 is stored); removing the last one turns
//     2FA off and deletes them;
//   - signing in with 2FA: the right password gives a challenge (5 minutes,
//     5 attempts) instead of a session; a current TOTP code, a security key
//     or an unused recovery code completes it. A TOTP code is accepted once (its time
//     step is recorded). Wrong codes count as failed logins (ratelimit);
//   - sudo: sensitive actions (2FA changes, new access tokens, SSH and deploy
//     keys, repository tokens) need a session confirmed in the last 10
//     minutes. Signing in counts; POST /auth/sudo confirms again, with a code
//     when 2FA is on, else with the password;
//   - personal access tokens and SSH keys keep working without codes (git,
//     CI);
//   - the user is emailed (when SMTP is set) when 2FA is turned on or off,
//     and when recovery codes are regenerated;
//   - site admins reset a user's 2FA (AdminApp.ResetTwoFactor; also `ogit
//     admin reset-2fa`).

var (
	ErrSudoRequired         = errors.New("confirm it's you first: POST /api/v1/auth/sudo with a code (or your password without two-factor authentication)")
	ErrInvalidCode          = errors.New("invalid or already used code")
	ErrChallengeExpired     = errors.New("the sign-in expired; sign in again")
	ErrTwoFactorEnabled     = errors.New("two-factor authentication is already on; turn it off first")
	ErrTwoFactorDisabled    = errors.New("two-factor authentication is off")
	ErrTwoFactorUnavailable = errors.New("authenticator apps are unavailable: the server has no security.encryption_key")
	ErrNoPendingSetup       = errors.New("start the authenticator app setup first")
)

const (
	SudoWindow        = 10 * time.Minute
	challengeLifetime = 5 * time.Minute
	challengeAttempts = 5
	recoveryCodeCount = 10
)

type TwoFactorApp struct {
	// IdPTrusted reports users who sign in only through providers whose
	// MFA is trusted (they count as having 2FA); nil: none.
	IdPTrusted func(userId string) (bool, error)
	// PasswordBlocked: passwords can't confirm sudo for user (only
	// sign-in providers); nil: never.
	PasswordBlocked func(user *domain.User) bool
	// DirectoryPassword: password is user's in a linked directory (it
	// confirms sudo too); nil: none.
	DirectoryPassword func(user *domain.User, password string) bool
	Config            config.Config
	Logger            zerolog.Logger
	Users             domain.UserPers
	Sessions          domain.SessionPers
	Pers              domain.TwoFactorPers
	Box               *secretbox.Box // nil without security.encryption_key
	Mailer            mail.Sender
	// WebAuthn is nil without http.public_url (its host is the relying
	// party): no security keys then.
	WebAuthn *webauthn.WebAuthn
	// Settings holds the admin's site-wide requirement (two_factor_required.go).
	Settings domain.SiteSettingPers
}

// TwoFactorStatus is what the settings page shows.
type TwoFactorStatus struct {
	Enabled           bool
	TOTP              bool // an authenticator app is set up
	Keys              []domain.WebAuthnCredential
	RecoveryCodesLeft int64
	TOTPAvailable     bool // the server can store authenticator secrets
	WebAuthnAvailable bool // the server has a public URL for security keys
}

func (c *TwoFactorApp) totpOn(userId string) (bool, error) {
	t, err := c.Pers.GetTOTP(userId)
	if isNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return t.ConfirmedAt != nil, nil
}

// Enabled: an authenticator app or a security key.
func (c *TwoFactorApp) Enabled(userId string) (bool, error) {
	if on, err := c.totpOn(userId); err != nil || on {
		return on, err
	}
	keys, err := c.Pers.ListCredentials(userId)
	return len(keys) > 0, err
}

// Methods lists the user's second factors: "totp", "webauthn".
func (c *TwoFactorApp) Methods(userId string) ([]string, error) {
	st, err := c.Status(userId)
	if err != nil {
		return nil, err
	}
	methods := []string{}
	if st.TOTP {
		methods = append(methods, "totp")
	}
	if len(st.Keys) > 0 && c.WebAuthn != nil {
		methods = append(methods, "webauthn")
	}
	return methods, nil
}

func (c *TwoFactorApp) Status(userId string) (*TwoFactorStatus, error) {
	totpOn, err := c.totpOn(userId)
	if err != nil {
		return nil, err
	}
	keys, err := c.Pers.ListCredentials(userId)
	if err != nil {
		return nil, err
	}
	on := totpOn || len(keys) > 0
	st := &TwoFactorStatus{Enabled: on, TOTP: totpOn, Keys: keys, TOTPAvailable: c.Box != nil, WebAuthnAvailable: c.WebAuthn != nil}
	if on {
		if st.RecoveryCodesLeft, err = c.Pers.CountRecoveryCodes(userId); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// sessionId reads the session of an API request.
func sessionId(auth *fiberoapi.AuthContext) string {
	if auth == nil {
		return ""
	}
	id, _ := auth.Claims[SessionIDClaim].(string)
	return id
}

// RequireSudo returns ErrSudoRequired unless the request's session was
// confirmed in the last SudoWindow.
func (c *TwoFactorApp) RequireSudo(auth *fiberoapi.AuthContext) error {
	id := sessionId(auth)
	if id == "" {
		return ErrSudoRequired
	}
	s, err := c.Sessions.GetById(id)
	if err != nil {
		return ErrSudoRequired
	}
	if s.SudoUntil == nil || !s.SudoUntil.After(time.Now()) {
		return ErrSudoRequired
	}
	return nil
}

// Sudo confirms the session again: with a code when 2FA is on, else with
// the password. Wrong answers return ErrInvalidCode or
// ErrInvalidCredentials (the caller counts them as failed logins).
func (c *TwoFactorApp) Sudo(auth *fiberoapi.AuthContext, code, password string, key *KeyAssertion) (time.Time, error) {
	id := sessionId(auth)
	if id == "" {
		return time.Time{}, ErrSudoRequired
	}
	on, err := c.Enabled(auth.UserID)
	if err != nil {
		return time.Time{}, err
	}
	if on && key != nil {
		if err := c.verifyKey(auth.UserID, ceremonySudo, key); err != nil {
			return time.Time{}, err
		}
	} else if on {
		if err := c.Verify(auth.UserID, code); err != nil {
			return time.Time{}, err
		}
	} else {
		user, err := c.Users.GetById(auth.UserID)
		if err != nil {
			return time.Time{}, err
		}
		directory := c.DirectoryPassword != nil && c.DirectoryPassword(&user, password)
		if !directory && c.PasswordBlocked != nil && c.PasswordBlocked(&user) {
			return time.Time{}, ErrPasswordLoginDisabled
		}
		if !directory && bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)) != nil {
			return time.Time{}, ErrInvalidCredentials
		}
	}
	until := time.Now().Add(SudoWindow)
	return until, c.Sessions.SetSudoUntil(id, until)
}

// TOTPSetup is a new authenticator secret, to scan or type.
type TOTPSetup struct {
	Secret string
	URI    string
}

// SetupTOTP starts (or restarts) adding an authenticator app. Needs sudo.
func (c *TwoFactorApp) SetupTOTP(auth *fiberoapi.AuthContext) (*TOTPSetup, error) {
	if err := c.RequireSudo(auth); err != nil {
		return nil, err
	}
	if c.Box == nil {
		return nil, ErrTwoFactorUnavailable
	}
	if on, err := c.totpOn(auth.UserID); err != nil {
		return nil, err
	} else if on {
		return nil, ErrTwoFactorEnabled
	}
	user, err := c.Users.GetById(auth.UserID)
	if err != nil {
		return nil, err
	}
	secret, err := totp.NewSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := c.Box.Seal(secret)
	if err != nil {
		return nil, err
	}
	if err := c.Pers.SaveTOTP(&domain.UserTOTP{UserId: user.Id, Secret: sealed}); err != nil {
		return nil, err
	}
	return &TOTPSetup{Secret: secret, URI: totp.URI("oGit", user.Username, secret)}, nil
}

// ConfirmTOTP confirms the app with its first code. When it turns 2FA on,
// it returns the recovery codes (shown once); with security keys already
// there, the existing codes stay and it returns none.
func (c *TwoFactorApp) ConfirmTOTP(userId, code string) ([]string, error) {
	wasOn, err := c.Enabled(userId)
	if err != nil {
		return nil, err
	}
	t, err := c.Pers.GetTOTP(userId)
	if isNotFound(err) {
		return nil, ErrNoPendingSetup
	} else if err != nil {
		return nil, err
	}
	if t.ConfirmedAt != nil {
		return nil, ErrTwoFactorEnabled
	}
	secret, err := c.Box.Open(t.Secret)
	if err != nil {
		return nil, err
	}
	step, ok := totp.Validate(secret, code, time.Now())
	if !ok {
		return nil, ErrInvalidCode
	}
	if err := c.Pers.ConfirmTOTP(userId, step, time.Now()); err != nil {
		return nil, err
	}
	c.audit(userId, "two_factor.totp_added").Msg("authenticator app added")
	if wasOn {
		c.notify(userId, "An authenticator app was added", "An authenticator app was added to your oGit account's two-factor authentication.")
		return []string{}, nil
	}
	return c.turnedOn(userId, "a code from your authenticator app")
}

// turnedOn creates the recovery codes of a user whose first second factor
// was just added, and tells them.
func (c *TwoFactorApp) turnedOn(userId, what string) ([]string, error) {
	codes, err := c.newRecoveryCodes(userId)
	if err != nil {
		return nil, err
	}
	c.audit(userId, "two_factor.enable").Msg("two-factor authentication turned on")
	c.notify(userId, "Two-factor authentication was turned on",
		"Two-factor authentication was turned on for your oGit account. Signing in now needs "+what+".")
	return codes, nil
}

// Disable turns 2FA off. Needs sudo.
func (c *TwoFactorApp) Disable(auth *fiberoapi.AuthContext) error {
	if err := c.RequireSudo(auth); err != nil {
		return err
	}
	if on, err := c.Enabled(auth.UserID); err != nil {
		return err
	} else if !on {
		return ErrTwoFactorDisabled
	}
	if err := c.Pers.Disable(auth.UserID); err != nil {
		return err
	}
	c.audit(auth.UserID, "two_factor.disable").Msg("two-factor authentication turned off")
	c.notify(auth.UserID, "Two-factor authentication was turned off",
		"Two-factor authentication was turned off for your oGit account. Signing in now only needs your password.")
	return nil
}

// RegenerateRecoveryCodes replaces the recovery codes. Needs sudo.
func (c *TwoFactorApp) RegenerateRecoveryCodes(auth *fiberoapi.AuthContext) ([]string, error) {
	if err := c.RequireSudo(auth); err != nil {
		return nil, err
	}
	if on, err := c.Enabled(auth.UserID); err != nil {
		return nil, err
	} else if !on {
		return nil, ErrTwoFactorDisabled
	}
	codes, err := c.newRecoveryCodes(auth.UserID)
	if err != nil {
		return nil, err
	}
	c.audit(auth.UserID, "two_factor.recovery_codes").Msg("recovery codes regenerated")
	c.notify(auth.UserID, "New recovery codes were generated",
		"New two-factor recovery codes were generated for your oGit account; the previous ones no longer work.")
	return codes, nil
}

var recoveryAlphabet = base32.NewEncoding("abcdefghijkmnpqrstuvwxyz23456789").WithPadding(base32.NoPadding)

// newRecoveryCodes stores 10 new codes ("xxxxx-xxxxx", 50 bits each).
func (c *TwoFactorApp) newRecoveryCodes(userId string) ([]string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	hashes := make([]string, 0, recoveryCodeCount)
	for range recoveryCodeCount {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		raw := recoveryAlphabet.EncodeToString(b)[:10]
		codes = append(codes, raw[:5]+"-"+raw[5:])
		hashes = append(hashes, hashRecoveryCode(raw))
	}
	return codes, c.Pers.ReplaceRecoveryCodes(userId, hashes)
}

func hashRecoveryCode(normalized string) string {
	sum := sha256.Sum256([]byte("ogit-recovery:" + normalized))
	return hex.EncodeToString(sum[:])
}

// Verify checks a second factor: a TOTP code (6 digits, each time step
// once) or an unused recovery code (used up).
func (c *TwoFactorApp) Verify(userId, code string) error {
	code = strings.ToLower(strings.Join(strings.Fields(code), ""))
	if code == "" {
		return ErrInvalidCode
	}
	if rc := strings.ReplaceAll(code, "-", ""); len(rc) == 10 {
		ok, err := c.Pers.UseRecoveryCode(userId, hashRecoveryCode(rc), time.Now())
		if err != nil {
			return err
		}
		if !ok {
			return ErrInvalidCode
		}
		c.audit(userId, "two_factor.recovery_code_used").Msg("a recovery code was used")
		return nil
	}
	t, err := c.Pers.GetTOTP(userId)
	if isNotFound(err) || (err == nil && t.ConfirmedAt == nil) {
		return ErrInvalidCode
	} else if err != nil {
		return err
	}
	if c.Box == nil {
		return ErrTwoFactorUnavailable
	}
	secret, err := c.Box.Open(t.Secret)
	if err != nil {
		c.Logger.Error().Err(err).Str("user_id", userId).Msg("cannot read a TOTP secret")
		return ErrInvalidCode
	}
	step, ok := totp.Validate(secret, code, time.Now())
	if !ok {
		return ErrInvalidCode
	}
	if fresh, err := c.Pers.UseStep(userId, step); err != nil {
		return err
	} else if !fresh {
		return ErrInvalidCode // replayed
	}
	return nil
}

// StartLogin records a login waiting for its second factor and returns
// the token the client sends back with the code.
func (c *TwoFactorApp) StartLogin(user *domain.User, userAgent, ip string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	err := c.Pers.CreateChallenge(&domain.LoginChallenge{TokenHash: HashToken(token), UserId: user.Id, UserAgent: userAgent, IpAddress: ip,
		ExpiresAt: time.Now().Add(challengeLifetime)})
	return token, err
}

// ChallengeUser returns the user a pending login is for (to rate-limit
// by account before checking the code).
func (c *TwoFactorApp) ChallengeUser(token string) (*domain.User, error) {
	ch, err := c.Pers.GetChallenge(HashToken(token))
	if err != nil || ch.ExpiresAt.Before(time.Now()) {
		return nil, ErrChallengeExpired
	}
	u, err := c.Users.GetById(ch.UserId)
	if err != nil {
		return nil, ErrChallengeExpired
	}
	return &u, nil
}

// CompleteLogin checks the code (or security key) of a pending login. The
// challenge is used up on success, and after challengeAttempts failures.
func (c *TwoFactorApp) CompleteLogin(token, code string, key *KeyAssertion) (*domain.User, *domain.LoginChallenge, error) {
	ch, err := c.Pers.GetChallenge(HashToken(token))
	if err != nil || ch.ExpiresAt.Before(time.Now()) {
		return nil, nil, ErrChallengeExpired
	}
	verify := func() error { return c.Verify(ch.UserId, code) }
	if key != nil {
		verify = func() error { return c.verifyKey(ch.UserId, ceremonyLogin, key) }
	}
	if err := verify(); err != nil {
		if !errors.Is(err, ErrInvalidCode) {
			return nil, nil, err
		}
		if n, ferr := c.Pers.FailChallenge(ch.Id); ferr == nil && n >= challengeAttempts {
			_ = c.Pers.DeleteChallenge(ch.Id)
		}
		return nil, nil, err
	}
	if err := c.Pers.DeleteChallenge(ch.Id); err != nil {
		return nil, nil, err
	}
	user, err := c.Users.GetById(ch.UserId)
	if err != nil {
		return nil, nil, err
	}
	if !user.Active {
		return nil, nil, ErrAccountDisabled
	}
	return &user, ch, nil
}

// EnabledUsers returns which of userIds have 2FA on (admin user list).
func (c *TwoFactorApp) EnabledUsers(userIds []string) (map[string]bool, error) {
	return c.Pers.EnabledUsers(userIds)
}

// Reset turns a user's 2FA off for an admin (see AdminApp.ResetTwoFactor)
// and tells the user.
func (c *TwoFactorApp) Reset(userId string) error {
	if err := c.Pers.Disable(userId); err != nil {
		return err
	}
	c.notify(userId, "Two-factor authentication was reset by an administrator",
		"An administrator turned off two-factor authentication for your oGit account, so you can sign in with your password and set it up again.")
	return nil
}

// DeleteExpiredChallenges runs with the session cleanup.
func (c *TwoFactorApp) DeleteExpiredChallenges() error {
	if _, err := c.Pers.DeleteExpiredCeremonies(time.Now()); err != nil {
		return err
	}
	_, err := c.Pers.DeleteExpiredChallenges(time.Now())
	return err
}

func (c *TwoFactorApp) audit(userId, action string) *zerolog.Event {
	return c.Logger.Info().Str("component", "audit").Str("actor_id", userId).Str("action", action)
}

func (c *TwoFactorApp) notify(userId, subject, text string) {
	if c.Config.SMTP.Host == "" || c.Mailer == nil {
		return
	}
	user, err := c.Users.GetById(userId)
	if err != nil {
		return
	}
	body := fmt.Sprintf("Hello %s,\n\n%s\n\nIf this wasn't you, change your password right away and contact your administrator.\n", user.Username, text)
	if err := c.Mailer.Send(user.Email, subject, body); err != nil {
		c.Logger.Error().Err(err).Str("user_id", userId).Msg("failed to send a two-factor notification")
	}
}
