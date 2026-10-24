package dtos

// Path fields are json:"-" so that a request body cannot override them
// (fiber-oapi parses the body after the path).

type RepositorySummary struct {
	Id          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
}

type ListRepositoriesResponse struct {
	Repositories []RepositorySummary `json:"repositories"`
}

type MemberResponse struct {
	Id       string `json:"id"`
	Type     string `json:"type"`               // "user" or "group"
	Username string `json:"username,omitempty"` // when type is user
	Group    string `json:"group,omitempty"`    // when type is group
	Role     string `json:"role"`
}

type ListMembersRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ListMembersResponse struct {
	Members []MemberResponse `json:"members"`
}

type AddMemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Username   string `json:"username"` // exactly one of username or group
	Group      string `json:"group"`
	Role       string `json:"role" validate:"required"`
}

type UpdateMemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	MemberId   string `path:"member_id" json:"-" validate:"required"`
	Role       string `json:"role" validate:"required"`
}

type RemoveMemberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	MemberId   string `path:"member_id" json:"-" validate:"required"`
}

type RemoveMemberResponse struct {
	Message string `json:"message"`
}
