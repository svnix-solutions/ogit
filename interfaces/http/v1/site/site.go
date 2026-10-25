// Package site serves the public pages' data: instance settings, public
// repositories and namespaces. These routes don't require a session, but
// use one when the request has it (a bearer token or the web UI's cookie).
package site

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config        config.Config
	Logger        zerolog.Logger
	SessionApp    *application.SessionApp
	RepositoryApp *application.RepositoryApp
}

type InstanceResponse struct {
	Version             string `json:"version"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	SSHEnabled          bool   `json:"ssh_enabled"`
	SSHPort             int    `json:"ssh_port,omitempty"`
}

type Repository struct {
	Id          string `json:"id"`
	Namespace   string `json:"namespace"`
	Slug        string `json:"slug"`
	FullPath    string `json:"full_path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsPrivate   bool   `json:"is_private"`
	IsArchived  bool   `json:"archived"`
}

type ExploreRequest struct {
	Query  string `query:"q" json:"-"`
	Limit  int    `query:"limit" json:"-"`
	Offset int    `query:"offset" json:"-"`
}

type ExploreResponse struct {
	Repositories []Repository `json:"repositories"`
	Total        int64        `json:"total"`
}

type NamespaceRequest struct {
	Name   string `path:"name" json:"-" validate:"required"`
	Offset int    `query:"offset" json:"-"`
	Limit  int    `query:"limit" json:"-"` // default 100, at most 100
}

type NamespaceResponse struct {
	Name         string       `json:"name"`
	Type         string       `json:"type"` // user or group
	Description  string       `json:"description,omitempty"`
	CanCreate    bool         `json:"can_create"`
	Repositories []Repository `json:"repositories"`
	Total        int          `json:"total"` // repositories visible to the caller
}

func Setup(c Controller, grp *fiberoapi.OApiGroup) {
	tags := []string{"Site"}
	fiberoapi.Get(grp, "/instance", c.Instance, fiberoapi.OpenAPIOptions{
		Summary: "Instance settings", OperationID: "site.instance", Tags: tags, Security: "disabled",
		Description: "What clients need to know: whether registration is open and git over SSH is enabled (and its port).",
	})
	fiberoapi.Get(grp, "/explore", c.Explore, fiberoapi.OpenAPIOptions{
		Summary: "Public repositories", OperationID: "site.explore", Tags: tags, Security: "disabled",
		Description: "Public repositories whose name or description contains ?q=, with ?limit= and ?offset=. No session needed.",
	})
	fiberoapi.Get(grp, "/namespace/:name", c.Namespace, fiberoapi.OpenAPIOptions{
		Summary: "A user's or group's repositories", OperationID: "site.namespace", Tags: tags, Security: "disabled",
		Description: "The repositories under a user or group that the caller can read (public ones without a session). Groups are not found unless the caller is a member or one of their repositories is visible.",
	})
}

func (c Controller) userId(ctx *fiber.Ctx) string { return optauth.UserID(ctx, c.SessionApp) }

func (c Controller) Instance(ctx *fiber.Ctx, _ struct{}) (*InstanceResponse, *fiberoapi.ErrorResponse) {
	resp := &InstanceResponse{
		Version:             c.Config.Version,
		RegistrationEnabled: c.Config.Registration.Enabled,
		SSHEnabled:          c.Config.SSH.Enabled,
	}
	if resp.SSHEnabled {
		resp.SSHPort = c.Config.SSH.Port
	}
	return resp, nil
}

func (c Controller) Explore(ctx *fiber.Ctx, req ExploreRequest) (*ExploreResponse, *fiberoapi.ErrorResponse) {
	repos, total, err := c.RepositoryApp.ListPublic(req.Query, req.Limit, req.Offset)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ExploreResponse{Repositories: summaries(repos), Total: total}, nil
}

func (c Controller) Namespace(ctx *fiber.Ctx, req NamespaceRequest) (*NamespaceResponse, *fiberoapi.ErrorResponse) {
	view, err := c.RepositoryApp.ViewNamespace(req.Name, c.userId(ctx))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	repos := view.Repositories
	start := min(max(req.Offset, 0), len(repos))
	end := min(start+limit, len(repos))
	return &NamespaceResponse{
		Name:         view.Path,
		Type:         string(view.Type),
		Description:  view.Description,
		CanCreate:    view.CanCreate,
		Repositories: summaries(repos[start:end]),
		Total:        len(repos),
	}, nil
}

func summaries(repos []domain.Repository) []Repository {
	out := []Repository{}
	for _, r := range repos {
		out = append(out, Repository{Id: r.Id, Namespace: r.Namespace, Slug: r.Slug, FullPath: r.FullPath(), Name: r.Name,
			Description: r.Description, IsPrivate: r.IsPrivate, IsArchived: r.IsArchived})
	}
	return out
}
