package application

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Security key and passkey rules (WebAuthn, see two_factor.go for the
// rest):
//   - the relying party is http.public_url's host (no public URL: no keys);
//   - adding a key needs sudo; it is a second factor, so the first one
//     turns 2FA on (with recovery codes). A key added "as a passkey" is
//     discoverable and verifies the user (PIN, biometrics), so it can also
//     sign in without the password; that counts as two factors;
//   - every ceremony's challenge is stored server-side (webauthn_ceremony),
//     bound to its user and purpose, used once, valid 5 minutes;
//   - an assertion whose signature counter went backwards (a cloned key) is
//     refused;
//   - deleting the last key (with no authenticator app) turns 2FA off.

var (
	ErrWebAuthnUnavailable = errors.New("security keys are unavailable: the server has no http.public_url")
	ErrInvalidKey          = errors.New("the security key's answer was not accepted; try again")
	ErrKeyNotFound         = errors.New("security key not found")
	ErrKeyNameTaken        = errors.New("you already have a key with this name")
)

const (
	ceremonyRegister = "register"
	ceremonyLogin    = "login" // second factor at sign-in
	ceremonySudo     = "sudo"
	ceremonyPasskey  = "passkey"
	ceremonyLifetime = 5 * time.Minute
)

// KeyAssertion is a security key's answer to a ceremony.
type KeyAssertion struct {
	Ceremony string
	Response []byte // the browser's PublicKeyCredential, as JSON
}

// NewWebAuthn configures the relying party from the public URL; nil
// without one.
func NewWebAuthn(publicURL string) (*webauthn.WebAuthn, error) {
	if publicURL == "" {
		return nil, nil
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("http.public_url is not a URL")
	}
	return webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "oGit",
		RPOrigins:     []string{u.Scheme + "://" + u.Host},
	})
}

// waUser adapts a user and their keys to the library.
type waUser struct {
	user  *domain.User
	creds []webauthn.Credential
	rows  []domain.WebAuthnCredential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(u.user.Id) }
func (u *waUser) WebAuthnName() string                       { return u.user.Username }
func (u *waUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (c *TwoFactorApp) loadUser(userId string) (*waUser, error) {
	user, err := c.Users.GetById(userId)
	if err != nil {
		return nil, err
	}
	rows, err := c.Pers.ListCredentials(userId)
	if err != nil {
		return nil, err
	}
	u := &waUser{user: &user, rows: rows}
	for _, r := range rows {
		var cred webauthn.Credential
		if err := json.Unmarshal([]byte(r.Data), &cred); err != nil {
			return nil, err
		}
		u.creds = append(u.creds, cred)
	}
	return u, nil
}

func credentialKey(id []byte) string { return base64.RawURLEncoding.EncodeToString(id) }

// saveCeremony stores the library's session data; the id goes to the
// client.
func (c *TwoFactorApp) saveCeremony(userId *string, kind string, session *webauthn.SessionData) (string, error) {
	data, err := json.Marshal(session)
	if err != nil {
		return "", err
	}
	cer := &domain.WebAuthnCeremony{UserId: userId, Kind: kind, Data: string(data), ExpiresAt: time.Now().Add(ceremonyLifetime)}
	if err := c.Pers.CreateCeremony(cer); err != nil {
		return "", err
	}
	return cer.Id, nil
}

// takeCeremony uses up a ceremony of kind for userId ("" for passkeys).
func (c *TwoFactorApp) takeCeremony(id, kind, userId string) (*webauthn.SessionData, error) {
	cer, err := c.Pers.TakeCeremony(id)
	if err != nil {
		return nil, ErrInvalidKey
	}
	owner := ""
	if cer.UserId != nil {
		owner = *cer.UserId
	}
	if cer.Kind != kind || owner != userId || cer.ExpiresAt.Before(time.Now()) {
		return nil, ErrInvalidKey
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(cer.Data), &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// KeyOptions is what the browser needs to run a ceremony.
type KeyOptions struct {
	Ceremony string
	Options  any // protocol.CredentialCreation or CredentialAssertion
}

// BeginKeyRegistration starts adding a key (a passkey when passkey is
// set). Needs sudo.
func (c *TwoFactorApp) BeginKeyRegistration(auth *fiberoapi.AuthContext, passkey bool) (*KeyOptions, error) {
	if err := c.RequireSudo(auth); err != nil {
		return nil, err
	}
	if c.WebAuthn == nil {
		return nil, ErrWebAuthnUnavailable
	}
	u, err := c.loadUser(auth.UserID)
	if err != nil {
		return nil, err
	}
	sel := protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementDiscouraged, UserVerification: protocol.VerificationPreferred}
	if passkey {
		sel = protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, UserVerification: protocol.VerificationRequired}
		sel.RequireResidentKey = protocol.ResidentKeyRequired()
	}
	creation, session, err := c.WebAuthn.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(sel),
		webauthn.WithExclusions(webauthn.Credentials(u.creds).CredentialDescriptors()),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
	if err != nil {
		return nil, err
	}
	kind := ceremonyRegister
	if passkey {
		kind += ":passkey"
	}
	id, err := c.saveCeremony(&auth.UserID, kind, session)
	if err != nil {
		return nil, err
	}
	return &KeyOptions{Ceremony: id, Options: creation}, nil
}

// FinishKeyRegistration stores the new key. When it turns 2FA on, it
// returns the recovery codes (shown once).
func (c *TwoFactorApp) FinishKeyRegistration(auth *fiberoapi.AuthContext, ceremony, name string, response []byte, passkey bool) (*domain.WebAuthnCredential, []string, error) {
	if err := c.RequireSudo(auth); err != nil {
		return nil, nil, err
	}
	if c.WebAuthn == nil {
		return nil, nil, ErrWebAuthnUnavailable
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, nil, ErrInvalidKey
	}
	kind := ceremonyRegister
	if passkey {
		kind += ":passkey"
	}
	session, err := c.takeCeremony(ceremony, kind, auth.UserID)
	if err != nil {
		return nil, nil, err
	}
	u, err := c.loadUser(auth.UserID)
	if err != nil {
		return nil, nil, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(response)
	if err != nil {
		return nil, nil, ErrInvalidKey
	}
	cred, err := c.WebAuthn.CreateCredential(u, *session, parsed)
	if err != nil {
		c.Logger.Info().Err(err).Str("user_id", auth.UserID).Msg("security key registration refused")
		return nil, nil, ErrInvalidKey
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return nil, nil, err
	}
	wasOn, err := c.Enabled(auth.UserID)
	if err != nil {
		return nil, nil, err
	}
	row := &domain.WebAuthnCredential{UserId: auth.UserID, Name: name, CredentialId: credentialKey(cred.ID), Passkey: passkey, Data: string(data)}
	if err := c.Pers.CreateCredential(row); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, nil, ErrKeyNameTaken
	} else if err != nil {
		return nil, nil, err
	}
	c.audit(auth.UserID, "two_factor.key_added").Str("key", name).Bool("passkey", passkey).Msg("security key added")
	if wasOn {
		c.notify(auth.UserID, "A security key was added", "The security key \""+name+"\" was added to your oGit account.")
		return row, []string{}, nil
	}
	codes, err := c.turnedOn(auth.UserID, "your security key")
	return row, codes, err
}

// DeleteKey removes a key. Needs sudo. Without any second factor left, 2FA
// is off and the recovery codes go.
func (c *TwoFactorApp) DeleteKey(auth *fiberoapi.AuthContext, id string) error {
	if err := c.RequireSudo(auth); err != nil {
		return err
	}
	ok, err := c.Pers.DeleteCredential(auth.UserID, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrKeyNotFound
	}
	c.audit(auth.UserID, "two_factor.key_removed").Str("key_id", id).Msg("security key removed")
	if on, err := c.Enabled(auth.UserID); err != nil {
		return err
	} else if !on {
		if err := c.Pers.DeleteRecoveryCodes(auth.UserID); err != nil {
			return err
		}
		c.audit(auth.UserID, "two_factor.disable").Msg("two-factor authentication turned off (last key removed)")
		c.notify(auth.UserID, "Two-factor authentication was turned off",
			"Your last security key was removed, so two-factor authentication is off for your oGit account.")
	}
	return nil
}

// beginAssertion starts a key check for userId (second factor, sudo).
func (c *TwoFactorApp) beginAssertion(userId, kind string) (*KeyOptions, error) {
	if c.WebAuthn == nil {
		return nil, ErrWebAuthnUnavailable
	}
	u, err := c.loadUser(userId)
	if err != nil {
		return nil, err
	}
	if len(u.creds) == 0 {
		return nil, ErrKeyNotFound
	}
	assertion, session, err := c.WebAuthn.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return nil, err
	}
	id, err := c.saveCeremony(&userId, kind, session)
	if err != nil {
		return nil, err
	}
	return &KeyOptions{Ceremony: id, Options: assertion}, nil
}

// BeginLoginKey starts the security key check of a pending login.
func (c *TwoFactorApp) BeginLoginKey(token string) (*KeyOptions, error) {
	user, err := c.ChallengeUser(token)
	if err != nil {
		return nil, err
	}
	return c.beginAssertion(user.Id, ceremonyLogin)
}

// BeginSudoKey starts a security key check to confirm the session.
func (c *TwoFactorApp) BeginSudoKey(auth *fiberoapi.AuthContext) (*KeyOptions, error) {
	return c.beginAssertion(auth.UserID, ceremonySudo)
}

// verifyKey checks a key's answer to a ceremony of kind for userId.
func (c *TwoFactorApp) verifyKey(userId, kind string, key *KeyAssertion) error {
	if c.WebAuthn == nil {
		return ErrWebAuthnUnavailable
	}
	session, err := c.takeCeremony(key.Ceremony, kind, userId)
	if err != nil {
		return err
	}
	u, err := c.loadUser(userId)
	if err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(key.Response)
	if err != nil {
		return ErrInvalidKey
	}
	cred, err := c.WebAuthn.ValidateLogin(u, *session, parsed)
	if err != nil {
		c.Logger.Info().Err(err).Str("user_id", userId).Msg("security key assertion refused")
		return ErrInvalidKey
	}
	return c.used(u, cred)
}

// used records a key's new signature counter, refusing a cloned key.
func (c *TwoFactorApp) used(u *waUser, cred *webauthn.Credential) error {
	if cred.Authenticator.CloneWarning {
		c.Logger.Warn().Str("component", "audit").Str("user_id", u.user.Id).Msg("security key refused: its counter went backwards (cloned?)")
		return ErrInvalidKey
	}
	for _, r := range u.rows {
		if r.CredentialId == credentialKey(cred.ID) {
			data, err := json.Marshal(cred)
			if err != nil {
				return err
			}
			return c.Pers.UpdateCredential(r.Id, string(data), time.Now())
		}
	}
	return ErrInvalidKey
}

// BeginPasskeyLogin starts a passwordless sign-in: any passkey of this site.
func (c *TwoFactorApp) BeginPasskeyLogin() (*KeyOptions, error) {
	if c.WebAuthn == nil {
		return nil, ErrWebAuthnUnavailable
	}
	assertion, session, err := c.WebAuthn.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, err
	}
	id, err := c.saveCeremony(nil, ceremonyPasskey, session)
	if err != nil {
		return nil, err
	}
	return &KeyOptions{Ceremony: id, Options: assertion}, nil
}

// FinishPasskeyLogin returns the user a passkey signed in. The key must
// have verified the user (PIN, biometrics): it stands for both factors.
func (c *TwoFactorApp) FinishPasskeyLogin(key *KeyAssertion) (*domain.User, error) {
	if c.WebAuthn == nil {
		return nil, ErrWebAuthnUnavailable
	}
	session, err := c.takeCeremony(key.Ceremony, ceremonyPasskey, "")
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(key.Response)
	if err != nil {
		return nil, ErrInvalidKey
	}
	var found *waUser
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		row, err := c.Pers.CredentialByCredentialId(credentialKey(rawID))
		if err != nil || !bytes.Equal([]byte(row.UserId), userHandle) {
			return nil, ErrInvalidKey
		}
		u, err := c.loadUser(row.UserId)
		if err != nil {
			return nil, err
		}
		found = u
		return u, nil
	}
	_, cred, err := c.WebAuthn.ValidatePasskeyLogin(handler, *session, parsed)
	if err != nil || found == nil {
		c.Logger.Info().Err(err).Msg("passkey sign-in refused")
		return nil, ErrInvalidKey
	}
	if !cred.Flags.UserVerified {
		return nil, ErrInvalidKey
	}
	if err := c.used(found, cred); err != nil {
		return nil, err
	}
	if !found.user.Active {
		return nil, ErrAccountDisabled
	}
	return found.user, nil
}
