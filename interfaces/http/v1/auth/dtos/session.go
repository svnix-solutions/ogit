package dtos

import "time"

type SessionResponse struct {
	ExpiresAt         time.Time `json:"expires_at,omitzero"`
	TwoFactorRequired bool      `json:"two_factor_required,omitempty"`
	Challenge         string    `json:"challenge,omitempty"`
}
