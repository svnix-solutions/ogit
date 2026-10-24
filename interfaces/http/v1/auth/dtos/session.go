package dtos

import "time"

type SessionResponse struct {
	ExpiresAt time.Time `json:"expires_at"`
}
