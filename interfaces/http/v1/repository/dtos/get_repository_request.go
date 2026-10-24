package dtos

type GetRepositoryRequest struct {
	// UUID or slug; the handler tells them apart. ("slug" is not a validator
	// tag, and fiber-oapi does not allow registering custom ones.)
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type GetRepositoryResponse struct {
	Id          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	IsArchived  bool   `json:"archived"`
}

type UpdateRepositoryRequest struct {
	Identifier  string  `path:"identifier" json:"-" validate:"required"`
	Name        *string `json:"name"`        // owners; the old slug keeps working
	Description *string `json:"description"` // maintainers and owners
	IsPrivate   *bool   `json:"is_private"`  // owners
	Archived    *bool   `json:"archived"`    // owners; archived repositories refuse pushes
}

type DeleteRepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Confirm    string `query:"confirm" json:"-"` // must equal the slug
}

type DeleteRepositoryResponse struct {
	Message string `json:"message"`
}
