package dtos

type CreateRepositoryRequest struct {
	// Namespace is the owner: your username (the default) or a group you
	// maintain or own.
	Namespace   string `json:"namespace"`
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
	IsPrivate   *bool  `json:"is_private"` // defaults to true when omitted
}

type CreateRepositoryResponse struct {
	Id        string `json:"id"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Slug      string `json:"slug"`
	FullPath  string `json:"full_path"` // <namespace>/<slug>, as in /<full_path>.git
}
