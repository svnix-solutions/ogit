package dtos

type ProfileResponse struct {
	Id       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Avatar   string `json:"avatar"`
	Role     string `json:"role"` // admin or user

	TwoFactorEnabled bool `json:"two_factor_enabled"`
	// TwoFactorRequired: the site requires 2FA and the user has none; the
	// API refuses everything but its setup until they add it.
	TwoFactorRequired bool `json:"two_factor_required"`
	// HasPassword: false for accounts made through a sign-in provider
	// (they confirm sudo through it, and may set a password).
	HasPassword bool `json:"has_password"`
	// TwoFactorSatisfied: oGit 2FA, or signing in only through providers
	// whose MFA is trusted (what admin pages and 2FA requirements check).
	TwoFactorSatisfied bool `json:"two_factor_satisfied"`
	// PasswordLogin: the password signs in and confirms sudo (false without
	// one, or when only sign-in providers are allowed).
	PasswordLogin bool `json:"password_login"`
}
