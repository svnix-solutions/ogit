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
	// IsMirror: a pull mirror (read-only, follows its remote).
	IsMirror bool `json:"is_mirror"`
	// RequiredApprovals before pull requests can be merged.
	RequiredApprovals int  `json:"required_approvals"`
	HasIssues         bool `json:"has_issues"`
	// WikiAccess: who edits the wiki (writers, readers) or off.
	WikiAccess string `json:"wiki_access"`
	// ForkOf is the repository this one was forked from, when the caller
	// can read it; IsFork is set even when they can't (or it was deleted:
	// then false).
	ForkOf *RepositoryRef `json:"fork_of,omitempty"`
	IsFork bool           `json:"is_fork"`
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
	// HasIssues: owners; turn issues on or off.
	HasIssues *bool `json:"has_issues"`
	// WikiAccess: maintainers and owners; writers (developers and up edit
	// the wiki), readers (everyone who can read it) or off.
	WikiAccess *string `json:"wiki_access"`
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

// RepositoryRef names a repository.
type RepositoryRef struct {
	Id       string `json:"id"`
	FullPath string `json:"full_path"`
}

type ForkRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Namespace  string `json:"namespace"` // your own when empty, or a group you maintain
	Name       string `json:"name"`      // the original's when empty
}

type ListForksRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ListForksResponse struct {
	Forks []RepositorySummary `json:"forks"`
}

type SyncRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Branch     string `json:"branch"` // the fork's default branch when empty
}

type SyncResponse struct {
	Branch  string `json:"branch"`
	Before  string `json:"before"` // empty when the branch was created
	After   string `json:"after"`
	Updated bool   `json:"updated"` // false when it was up to date
}
