package dtos

type GetRepositoryRequest struct {
	// UUID or <namespace>/<slug> with the slash URL-encoded (alice%2Fsite);
	// see repoid.Split.
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type GetRepositoryResponse struct {
	Id          string `json:"id"`
	Namespace   string `json:"namespace"`
	OwnerType   string `json:"owner_type"` // user or group
	Slug        string `json:"slug"`
	FullPath    string `json:"full_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	IsArchived  bool   `json:"archived"`
	// RequiredApprovals before pull requests can be merged.
	RequiredApprovals int `json:"required_approvals"`
	// Role is the caller's effective role (none, reporter, developer,
	// maintainer, owner); only on GET.
	Role string `json:"role,omitempty"`
}

type UpdateRepositoryRequest struct {
	Identifier  string  `path:"identifier" json:"-" validate:"required"`
	Name        *string `json:"name"`        // owners; the old path keeps working
	Description *string `json:"description"` // maintainers and owners
	IsPrivate   *bool   `json:"is_private"`  // owners
	Archived    *bool   `json:"archived"`    // owners; archived repositories refuse pushes
	// RequiredApprovals: owners; approvals a pull request needs (0-10).
	RequiredApprovals *int `json:"required_approvals"`
}

type DeleteRepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Confirm    string `query:"confirm" json:"-"` // must equal the slug
}

type DeleteRepositoryResponse struct {
	Message string `json:"message"`
}

type TransferRepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Namespace  string `json:"namespace" validate:"required"` // a username or group name
}
