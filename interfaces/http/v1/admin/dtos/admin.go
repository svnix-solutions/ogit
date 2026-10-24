package dtos

import "time"

// Path and query fields are json:"-" so that a request body cannot override them.

type UserResponse struct {
	Id              string     `json:"id"`
	Username        string     `json:"username"`
	Email           string     `json:"email"`
	Role            string     `json:"role"`
	Active          bool       `json:"active"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type ListUsersRequest struct {
	Query  string `query:"q" json:"-"` // matches username or email
	Limit  int    `query:"limit" json:"-"`
	Offset int    `query:"offset" json:"-"`
}

type ListUsersResponse struct {
	Users []UserResponse `json:"users"`
	Total int64          `json:"total"`
}

type CreateUserRequest struct {
	Username string `json:"username" validate:"required"`
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type UpdateUserRequest struct {
	Username      string  `path:"username" json:"-" validate:"required"`
	Active        *bool   `json:"active"`
	EmailVerified *bool   `json:"email_verified"`
	Role          *string `json:"role"` // admin or user
}

type UserRequest struct {
	Username string `path:"username" json:"-" validate:"required"`
}

type RepositoryResponse struct {
	Id        string    `json:"id"`
	Namespace string    `json:"namespace"`
	Slug      string    `json:"slug"`
	FullPath  string    `json:"full_path"`
	Name      string    `json:"name"`
	IsPrivate bool      `json:"is_private"`
	CreatedAt time.Time `json:"created_at"`
}

type ListRepositoriesRequest struct {
	Limit  int `query:"limit" json:"-"`
	Offset int `query:"offset" json:"-"`
}

type ListRepositoriesResponse struct {
	Repositories []RepositoryResponse `json:"repositories"`
	Total        int64                `json:"total"`
}

type SetOwnerRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `json:"username" validate:"required"`
}

type SetOwnerResponse struct {
	MemberId string `json:"member_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type DeleteRepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Confirm    string `query:"confirm" json:"-"` // must equal the slug
}

type TransferGroupRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `json:"username" validate:"required"`
}

type GroupResponse struct {
	Id      string `json:"id"`
	Name    string `json:"name"`
	OwnerId string `json:"owner_id"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

type TransferRepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Namespace  string `json:"namespace" validate:"required"`
}
