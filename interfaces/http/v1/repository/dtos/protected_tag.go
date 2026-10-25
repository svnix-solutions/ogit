package dtos

import "time"

type ProtectedTagResponse struct {
	Id            string    `json:"id"`
	Pattern       string    `json:"pattern"`
	AllowedUsers  []string  `json:"allowed_users"`
	AllowedGroups []string  `json:"allowed_groups"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ListProtectedTagsResponse struct {
	Rules []ProtectedTagResponse `json:"rules"`
}

// ProtectedTagFields are a rule's settings; omitted ones are kept on
// update.
type ProtectedTagFields struct {
	Pattern       *string  `json:"pattern"`
	AllowedUsers  []string `json:"allowed_users"`  // usernames who may create matching tags, besides maintainers
	AllowedGroups []string `json:"allowed_groups"` // group names
}

type CreateProtectedTagRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	ProtectedTagFields
}

type ProtectedTagRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RuleId     string `path:"rule_id" json:"-" validate:"required"`
}

type UpdateProtectedTagRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RuleId     string `path:"rule_id" json:"-" validate:"required"`
	ProtectedTagFields
}
