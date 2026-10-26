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
}
