package dtos

// Path fields are json:"-" so that a request body cannot override them.

type CreateGroupRequest struct {
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
	// Parent makes a sub-group of this group (its path or id): the new
	// group's path is <parent>/<name>.
	Parent string `json:"parent"`
}

type GroupResponse struct {
	Id          string `json:"id"`
	Name        string `json:"name"` // the full path, org/team for a sub-group
	Parent      string `json:"parent,omitempty"`
	Description string `json:"description"`
	Role        string `json:"role,omitempty"` // the caller's role in the group
	// RequireTwoFactor: members without 2FA get no access to the group's
	// repositories through it.
	RequireTwoFactor bool `json:"require_two_factor"`
	// RequireTwoFactorInherited: an ancestor group requires it, so it
	// applies here too (whatever require_two_factor says).
	RequireTwoFactorInherited bool `json:"require_two_factor_inherited"`
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
	// InheritedFrom: the ancestor group the role comes from (a member of
	// it); empty for the group's own members.
	InheritedFrom string `json:"inherited_from,omitempty"`
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

type ListSubgroupsResponse struct {
	Groups []GroupResponse `json:"groups"`
}

type MoveGroupRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	// Parent: the group to move into (its path or id); "" for the top
	// level; omitted to stay under the same parent.
	Parent *string `json:"parent"`
	// Name: the group's new name (its last path segment); omitted to keep
	// it.
	Name string `json:"name"`
}
