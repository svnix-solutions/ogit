package dtos

import "time"

type CreateTokenRequest struct {
	Name        string     `json:"name" validate:"required"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes" validate:"required,min=1"`
	ExpiresAt   *time.Time `json:"expires_at"`
}

type CreateTokenResponse struct {
	Id        string     `json:"id"`
	Name      string     `json:"name"`
	Token     string     `json:"token"` // only returned once
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type TokenSummary struct {
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ListTokensResponse struct {
	Tokens []TokenSummary `json:"tokens"`
}

type RevokeTokenRequest struct {
	TokenId string `path:"token_id" json:"-" validate:"required"`
}

type RevokeTokenResponse struct {
	Message string `json:"message"`
}
