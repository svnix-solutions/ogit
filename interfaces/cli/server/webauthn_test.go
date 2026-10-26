package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/labbs/ogit/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// softKey is a software authenticator (ES256, "none" attestation), so the
// tests run real WebAuthn ceremonies against the server's verification.
type softKey struct {
	id         []byte
	priv       *ecdsa.PrivateKey
	userHandle []byte
	count      uint32
	origin     string
	uv         bool // the key verifies the user (PIN, biometrics)
}

const testOrigin = "https://git.example.com" // withPublicURL

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{id: id, priv: priv, origin: testOrigin, uv: true}
}

var b64 = base64.RawURLEncoding

// options reads the challenge and relying party from server options.
func options(t *testing.T, raw any) (challenge, rpID string, userID []byte) {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var o struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	require.NoError(t, json.Unmarshal(data, &o))
	rpID = o.PublicKey.RPID
	if rpID == "" {
		rpID = o.PublicKey.RP.ID
	}
	if o.PublicKey.User.ID != "" {
		userID, err = b64.DecodeString(o.PublicKey.User.ID)
		require.NoError(t, err)
	}
	return o.PublicKey.Challenge, rpID, userID
}

func (k *softKey) clientData(typ, challenge string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": k.origin, "crossOrigin": false})
	return b
}

func (k *softKey) authData(rpID string, attested bool) []byte {
	rp := sha256.Sum256([]byte(rpID))
	flags := byte(0x01) // user present
	if k.uv {
		flags |= 0x04
	}
	if attested {
		flags |= 0x40
	}
	k.count++
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, k.count)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
		x, y := make([]byte, 32), make([]byte, 32)
		k.priv.PublicKey.X.FillBytes(x)
		k.priv.PublicKey.Y.FillBytes(y)
		cose, _ := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
			PublicKeyData: webauthncose.PublicKeyData{KeyType: int64(webauthncose.EllipticKey), Algorithm: int64(webauthncose.AlgES256)},
			Curve:         1, XCoord: x, YCoord: y,
		})
		out = append(out, cose...)
	}
	return out
}

// register answers creation options (navigator.credentials.create).
func (k *softKey) register(t *testing.T, opts any) json.RawMessage {
	t.Helper()
	challenge, rpID, userID := options(t, opts)
	k.userHandle = userID
	att, err := webauthncbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(rpID, true)})
	require.NoError(t, err)
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(k.clientData("webauthn.create", challenge)), "attestationObject": b64.EncodeToString(att)},
	})
	return out
}

// assert answers assertion options (navigator.credentials.get).
func (k *softKey) assert(t *testing.T, opts any) json.RawMessage {
	t.Helper()
	challenge, rpID, _ := options(t, opts)
	auth := k.authData(rpID, false)
	cd := k.clientData("webauthn.get", challenge)
	hash := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, auth...), hash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.priv, digest[:])
	require.NoError(t, err)
	out, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(auth),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(k.userHandle)},
	})
	return out
}

// addKey registers k for as and returns the response (recovery codes when
// it turned 2FA on).
func (s *testServer) addKey(t *testing.T, as user, k *softKey, name string, passkey bool) map[string]any {
	t.Helper()
	status, begin := s.post(t, "/api/v1/user/2fa/webauthn", as.session, map[string]any{"passkey": passkey})
	require.Equal(t, http.StatusOK, status, "%v", begin)
	status, body := s.post(t, "/api/v1/user/2fa/webauthn/finish", as.session, map[string]any{
		"ceremony": begin["ceremony"], "name": name, "passkey": passkey, "credential": k.register(t, begin["options"])})
	require.Equal(t, http.StatusOK, status, "%v", body)
	return body
}

func TestSecurityKeys(t *testing.T) {
	srv := startTestServer(t, withPublicURL)
	alice := srv.newUser(t, "alice")
	key := newSoftKey(t)

	t.Run("register", func(t *testing.T) {
		_, st := srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.Equal(t, true, st["webauthn_available"])
		body := srv.addKey(t, alice, key, "YubiKey", false)
		assert.Len(t, body["recovery_codes"], 10, "the first second factor turns 2FA on")
		assert.Equal(t, "YubiKey", body["key"].(map[string]any)["name"])
		_, st = srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.Equal(t, true, st["enabled"])
		assert.Equal(t, false, st["totp"])
		assert.Len(t, st["keys"], 1)

		// A ceremony is single-use, bound to its purpose, and the answer must
		// match the challenge.
		status, begin := srv.post(t, "/api/v1/user/2fa/webauthn", alice.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		other := newSoftKey(t)
		answer := other.register(t, begin["options"])
		status, body = srv.post(t, "/api/v1/user/2fa/webauthn/finish", alice.session, map[string]any{"ceremony": begin["ceremony"], "name": "Other", "credential": answer})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Empty(t, body["recovery_codes"], "2FA was already on")
		status, body = srv.post(t, "/api/v1/user/2fa/webauthn/finish", alice.session, map[string]any{"ceremony": begin["ceremony"], "name": "Again", "credential": answer})
		assert.Equal(t, http.StatusUnauthorized, status, "replayed ceremony: %v", body)

		status, begin = srv.post(t, "/api/v1/user/2fa/webauthn", alice.session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		wrong := newSoftKey(t)
		wrong.origin = "https://evil.example"
		status, body = srv.post(t, "/api/v1/user/2fa/webauthn/finish", alice.session, map[string]any{"ceremony": begin["ceremony"], "name": "Phished", "credential": wrong.register(t, begin["options"])})
		assert.Equal(t, http.StatusUnauthorized, status, "another site's origin: %v", body)

		srv.expireSudo(t, "alice")
		status, body = srv.post(t, "/api/v1/user/2fa/webauthn", alice.session, map[string]any{})
		assert.Equal(t, "SUDO_REQUIRED", body["type"], "%d", status)
	})

	t.Run("second_factor", func(t *testing.T) {
		_, login := srv.login(t, "alice@example.com")
		require.Equal(t, true, login["two_factor_required"])
		assert.Equal(t, []any{"webauthn"}, login["methods"], "keys only, no app")
		ch := login["challenge"]
		status, begin := srv.post(t, "/api/v1/auth/login/2fa/webauthn", "", map[string]any{"challenge": ch})
		require.Equal(t, http.StatusOK, status, "%v", begin)

		stranger := newSoftKey(t)
		stranger.userHandle = []byte("someone")
		status, body := srv.post(t, "/api/v1/auth/login/2fa", "", map[string]any{"challenge": ch, "ceremony": begin["ceremony"], "credential": stranger.assert(t, begin["options"])})
		assert.Equal(t, http.StatusUnauthorized, status, "an unknown key: %v", body)

		status, begin = srv.post(t, "/api/v1/auth/login/2fa/webauthn", "", map[string]any{"challenge": ch})
		require.Equal(t, http.StatusOK, status)
		status, body = srv.post(t, "/api/v1/auth/login/2fa", "", map[string]any{"challenge": ch, "ceremony": begin["ceremony"], "credential": key.assert(t, begin["options"])})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.NotEmpty(t, body["token"])
		_, st := srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.NotNil(t, st["keys"].([]any)[0].(map[string]any)["last_used_at"])
	})

	t.Run("cloned_key", func(t *testing.T) {
		clone := *key
		clone.count = 0 // a copy whose counter is behind the original's
		_, login := srv.login(t, "alice@example.com")
		_, begin := srv.post(t, "/api/v1/auth/login/2fa/webauthn", "", map[string]any{"challenge": login["challenge"]})
		status, body := srv.post(t, "/api/v1/auth/login/2fa", "", map[string]any{"challenge": login["challenge"], "ceremony": begin["ceremony"], "credential": clone.assert(t, begin["options"])})
		assert.Equal(t, http.StatusUnauthorized, status, "counter went backwards: %v", body)
	})

	t.Run("sudo", func(t *testing.T) {
		srv.expireSudo(t, "alice")
		status, begin := srv.post(t, "/api/v1/auth/sudo/webauthn", alice.session, nil)
		require.Equal(t, http.StatusOK, status, "%v", begin)
		status, body := srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"ceremony": begin["ceremony"], "credential": key.assert(t, begin["options"])})
		require.Equal(t, http.StatusOK, status, "%v", body)
		status, _ = srv.post(t, "/api/v1/user/token", alice.session, map[string]any{"name": "after-key", "scopes": []string{"read:repository"}})
		assert.Equal(t, http.StatusOK, status)

		// A login ceremony can't confirm sudo.
		srv.expireSudo(t, "alice")
		_, login := srv.login(t, "alice@example.com")
		_, begin = srv.post(t, "/api/v1/auth/login/2fa/webauthn", "", map[string]any{"challenge": login["challenge"]})
		status, body = srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"ceremony": begin["ceremony"], "credential": key.assert(t, begin["options"])})
		assert.Equal(t, http.StatusUnauthorized, status, "%v", body)
	})

	t.Run("remove_keys", func(t *testing.T) {
		_, st := srv.get(t, "/api/v1/user/2fa", alice.session)
		srv.expireSudo(t, "alice")
		_, begin := srv.post(t, "/api/v1/auth/sudo/webauthn", alice.session, nil)
		status, _ := srv.post(t, "/api/v1/auth/sudo", alice.session, map[string]any{"ceremony": begin["ceremony"], "credential": key.assert(t, begin["options"])})
		require.Equal(t, http.StatusOK, status)
		for _, k := range st["keys"].([]any) {
			status, body := srv.request(t, http.MethodDelete, "/api/v1/user/2fa/webauthn/"+k.(map[string]any)["id"].(string), alice.session, nil)
			require.Equal(t, http.StatusOK, status, "%v", body)
		}
		_, st = srv.get(t, "/api/v1/user/2fa", alice.session)
		assert.Equal(t, false, st["enabled"], "no second factor left")
		var n int64
		require.NoError(t, srv.deps.Database.Db.Model(&domain.RecoveryCode{}).Where("used_at IS NULL").Count(&n).Error)
		assert.Zero(t, n, "recovery codes go with the last factor")
		status, body := srv.login(t, "alice@example.com")
		assert.NotEmpty(t, body["token"], "%d: the password alone again", status)
	})
}

func TestPasskeySignIn(t *testing.T) {
	srv := startTestServer(t, withPublicURL)
	bob := srv.newUser(t, "bob")
	passkey := newSoftKey(t)
	srv.addKey(t, bob, passkey, "Phone", true)
	plain := newSoftKey(t)
	srv.addKey(t, bob, plain, "USB key", false)

	signIn := func(t *testing.T, k *softKey) (int, map[string]any) {
		t.Helper()
		status, begin := srv.post(t, "/api/v1/auth/passkey", "", nil)
		require.Equal(t, http.StatusOK, status, "%v", begin)
		return srv.post(t, "/api/v1/auth/passkey/finish", "", map[string]any{"ceremony": begin["ceremony"], "credential": k.assert(t, begin["options"])})
	}

	status, body := signIn(t, passkey)
	require.Equal(t, http.StatusOK, status, "%v", body)
	token := body["token"].(string)
	_, profile := srv.get(t, "/api/v1/user/profile", token)
	assert.Equal(t, "bob", profile["username"])
	status, _ = srv.post(t, "/api/v1/user/token", token, map[string]any{"name": "from-passkey", "scopes": []string{"read:repository"}})
	assert.Equal(t, http.StatusOK, status, "a passkey sign-in counts as two factors, and starts in sudo")

	unverified := *passkey
	unverified.uv = false
	status, body = signIn(t, &unverified)
	assert.Equal(t, http.StatusUnauthorized, status, "a passkey must verify the user: %v", body)

	forged := *passkey
	forged.userHandle = []byte("another-user-id")
	status, body = signIn(t, &forged)
	assert.Equal(t, http.StatusUnauthorized, status, "the user handle must be the key's owner: %v", body)

	status, begin := srv.post(t, "/api/v1/auth/passkey", "", nil)
	require.Equal(t, http.StatusOK, status)
	answer := passkey.assert(t, begin["options"])
	status, _ = srv.post(t, "/api/v1/auth/passkey/finish", "", map[string]any{"ceremony": begin["ceremony"], "credential": answer})
	require.Equal(t, http.StatusOK, status)
	status, _ = srv.post(t, "/api/v1/auth/passkey/finish", "", map[string]any{"ceremony": begin["ceremony"], "credential": answer})
	assert.Equal(t, http.StatusUnauthorized, status, "a ceremony is used once")

	status, body, h := srv.do(t, http.MethodPost, "/api/v1/auth/passkey", nil, nil)
	require.Equal(t, http.StatusOK, status)
	status, body, h = srv.do(t, http.MethodPost, "/api/v1/auth/session/passkey", nil, map[string]any{"ceremony": body["ceremony"], "credential": passkey.assert(t, body["options"])})
	require.Equal(t, http.StatusOK, status, "%v", body)
	assert.Contains(t, h.Get("Set-Cookie"), "ogit_session=")

	// Disabled accounts can't sign in with a passkey either.
	require.NoError(t, srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "bob").Update("active", false).Error)
	status, body = signIn(t, passkey)
	assert.Equal(t, http.StatusForbidden, status, "%v", body)
}

func TestSecurityKeysNeedAPublicURL(t *testing.T) {
	srv := startTestServer(t)
	carol := srv.newUser(t, "carol")
	_, st := srv.get(t, "/api/v1/user/2fa", carol.session)
	assert.Equal(t, false, st["webauthn_available"])
	status, body := srv.post(t, "/api/v1/user/2fa/webauthn", carol.session, nil)
	assert.Equal(t, "WEBAUTHN_UNAVAILABLE", body["type"], "%d", status)
	_, inst := srv.get(t, "/api/v1/instance", "")
	assert.Equal(t, false, inst["passkeys"])
}
