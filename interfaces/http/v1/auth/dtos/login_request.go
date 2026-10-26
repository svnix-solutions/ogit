package dtos

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// LoginResponse has the token, or, for accounts with two-factor
// authentication, two_factor_required and a challenge to send back with a
// code to /auth/login/2fa (5 minutes, 5 attempts).
type LoginResponse struct {
	Token             string `json:"token,omitempty"`
	TwoFactorRequired bool   `json:"two_factor_required,omitempty"`
	Challenge         string `json:"challenge,omitempty"`
}

type TwoFactorLoginRequest struct {
	Challenge string `json:"challenge" validate:"required"`
	Code      string `json:"code" validate:"required"` // authenticator code or recovery code
}

type SudoRequest struct {
	Code     string `json:"code"`     // with two-factor authentication
	Password string `json:"password"` // without
}

type SudoResponse struct {
	Until string `json:"until"`
}
