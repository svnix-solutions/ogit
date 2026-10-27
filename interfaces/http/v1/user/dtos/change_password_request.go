package dtos

type ChangePasswordRequest struct {
	// CurrentPassword: empty for accounts without a password (made through
	// a sign-in provider), which set one with sudo instead.
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password" validate:"required"`
}

type ChangePasswordResponse struct {
	Message string `json:"message"`
}
