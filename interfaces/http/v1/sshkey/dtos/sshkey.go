package dtos

import "time"

// Path fields are json:"-" so that a request body cannot override them.

type SSHKeyResponse struct {
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	Fingerprint string     `json:"fingerprint"`
	Type        string     `json:"type"`
	ReadOnly    *bool      `json:"read_only,omitempty"` // deploy keys only
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ListSSHKeysResponse struct {
	Keys []SSHKeyResponse `json:"keys"`
}

type AddUserKeyRequest struct {
	Name string `json:"name"` // defaults to the key's comment
	Key  string `json:"key" validate:"required"`
}

type DeleteUserKeyRequest struct {
	KeyId string `path:"key_id" json:"-" validate:"required"`
}

type RepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type AddDeployKeyRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `json:"name"`
	Key        string `json:"key" validate:"required"`
	ReadOnly   *bool  `json:"read_only"` // defaults to true
}

type DeleteDeployKeyRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	KeyId      string `path:"key_id" json:"-" validate:"required"`
}

type AdminUserKeysRequest struct {
	Username string `path:"username" json:"-" validate:"required"`
}

type AdminDeleteUserKeyRequest struct {
	Username string `path:"username" json:"-" validate:"required"`
	KeyId    string `path:"key_id" json:"-" validate:"required"`
}

type MessageResponse struct {
	Message string `json:"message"`
}
