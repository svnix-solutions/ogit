package dtos

type GetRepositoryRequest struct {
	// UUID or slug; the handler tells them apart. ("slug" is not a validator
	// tag, and fiber-oapi does not allow registering custom ones.)
	Identifier string `path:"identifier" validate:"required"`
}

type GetRepositoryResponse struct {
	Id          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
}
