package dtos

// Path fields are json:"-" so that a request body cannot override them
// (fiber-oapi parses the body after the path).

type RepositorySummary struct {
	Id          string `json:"id"`
	Namespace   string `json:"namespace"`
	Slug        string `json:"slug"`
	FullPath    string `json:"full_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	IsArchived  bool   `json:"archived"`
}

type ListRepositoriesRequest struct {
	Scope  string `query:"scope" json:"-"` // "mine" (default) or "public"
	Query  string `query:"q" json:"-"`     // public scope: matches name or description
	Limit  int    `query:"limit" json:"-"`
	Offset int    `query:"offset" json:"-"`
}

type ListRepositoriesResponse struct {
	Repositories []RepositorySummary `json:"repositories"`
	Total        *int64              `json:"total,omitempty"` // public scope
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
