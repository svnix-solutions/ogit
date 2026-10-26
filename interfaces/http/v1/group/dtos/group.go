package dtos

// Path fields are json:"-" so that a request body cannot override them.

type CreateGroupRequest struct {
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
}

type GroupResponse struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Role        string `json:"role,omitempty"` // the caller's role in the group
	// RequireTwoFactor: members without 2FA get no access to the group's
	// repositories through it.
	RequireTwoFactor bool `json:"require_two_factor"`
}

type UpdateGroupRequest struct {
	Identifier       string `path:"identifier" json:"-" validate:"required"`
	RequireTwoFactor *bool  `json:"require_two_factor"`
}

type ListGroupsResponse struct {
	Groups []GroupResponse `json:"groups"`
}

type GroupRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"` // group id or name
}

type MemberResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	// TwoFactorEnabled is only shown to owners and maintainers.
	TwoFactorEnabled *bool `json:"two_factor_enabled,omitempty"`
}

type ListMembersResponse struct {
	Members []MemberResponse `json:"members"`
}

type AddMemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `json:"username" validate:"required"`
	Role       string `json:"role" validate:"required"` // member or maintainer
}

type UpdateMemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `path:"username" json:"-" validate:"required"`
	Role       string `json:"role" validate:"required"`
}

type MemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `path:"username" json:"-" validate:"required"`
}

type TransferRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `json:"username" validate:"required"` // new owner, must be a member
}

type MessageResponse struct {
	Message string `json:"message"`
}
