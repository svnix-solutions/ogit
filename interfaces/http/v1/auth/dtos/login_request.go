package dtos

import "encoding/json"

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// LoginResponse has the token, or, for accounts with two-factor
// authentication, two_factor_required and a challenge to send back with a
// code to /auth/login/2fa (5 minutes, 5 attempts).
type LoginResponse struct {
	Token             string   `json:"token,omitempty"`
	TwoFactorRequired bool     `json:"two_factor_required,omitempty"`
	Challenge         string   `json:"challenge,omitempty"`
	Methods           []string `json:"methods,omitempty"` // totp, webauthn (recovery codes always work)
}

// TwoFactorLoginRequest completes a login: a code, or a security key's
// answer (ceremony from /auth/login/2fa/webauthn, credential from the
// browser).
type TwoFactorLoginRequest struct {
	Challenge  string          `json:"challenge" validate:"required"`
	Code       string          `json:"code"` // authenticator code or recovery code
	Ceremony   string          `json:"ceremony"`
	Credential json.RawMessage `json:"credential"`
}

type SudoRequest struct {
	Code       string          `json:"code"`     // with two-factor authentication
	Password   string          `json:"password"` // without
	Ceremony   string          `json:"ceremony"` // or a security key (/auth/sudo/webauthn)
	Credential json.RawMessage `json:"credential"`
}

type ChallengeRequest struct {
	Challenge string `json:"challenge" validate:"required"`
}

// KeyOptionsResponse starts a WebAuthn ceremony: pass options to
// navigator.credentials (after decoding its base64url fields) and send the
// result back with ceremony.
type KeyOptionsResponse struct {
	Ceremony string          `json:"ceremony"`
	Options  json.RawMessage `json:"options"`
}

type PasskeyRequest struct {
	Ceremony   string          `json:"ceremony" validate:"required"`
	Credential json.RawMessage `json:"credential" validate:"required"`
}

type SudoResponse struct {
	Until string `json:"until"`
}
