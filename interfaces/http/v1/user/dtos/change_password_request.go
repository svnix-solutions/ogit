package dtos

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" validate:"required"`
	NewPassword     string `json:"new_password" validate:"required"`
}

type ChangePasswordResponse struct {
	Message string `json:"message"`
}
