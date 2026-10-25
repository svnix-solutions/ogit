package dtos

import "time"

type BranchProtectionResponse struct {
	Id                    string    `json:"id"`
	Pattern               string    `json:"pattern"`
	AllowForcePush        bool      `json:"allow_force_push"`
	AllowDeletion         bool      `json:"allow_deletion"`
	RequirePullRequest    bool      `json:"require_pull_request"`
	RequiredApprovals     *int      `json:"required_approvals"` // null: the repository's
	DismissStaleApprovals bool      `json:"dismiss_stale_approvals"`
	AllowedUsers          []string  `json:"allowed_users"`
	AllowedGroups         []string  `json:"allowed_groups"`
	OwnersBypass          bool      `json:"owners_bypass"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type ListBranchProtectionRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ListBranchProtectionResponse struct {
	Rules []BranchProtectionResponse `json:"rules"`
}

// BranchProtectionFields are a rule's settings; omitted ones are kept on
// update (and false or empty on create). required_approvals -1 means the
// repository's.
type BranchProtectionFields struct {
	Pattern               *string  `json:"pattern"`
	AllowForcePush        *bool    `json:"allow_force_push"`
	AllowDeletion         *bool    `json:"allow_deletion"`
	RequirePullRequest    *bool    `json:"require_pull_request"`
	RequiredApprovals     *int     `json:"required_approvals"`
	DismissStaleApprovals *bool    `json:"dismiss_stale_approvals"`
	AllowedUsers          []string `json:"allowed_users"`  // usernames
	AllowedGroups         []string `json:"allowed_groups"` // group names
	OwnersBypass          *bool    `json:"owners_bypass"`
}

type CreateBranchProtectionRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	BranchProtectionFields
}

type BranchProtectionRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RuleId     string `path:"rule_id" json:"-" validate:"required"`
}

type UpdateBranchProtectionRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	RuleId     string `path:"rule_id" json:"-" validate:"required"`
	BranchProtectionFields
}
