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
	"net/url"
)

type Controller struct {
	Config        config.Config
	Logger        zerolog.Logger
	SessionApp    *application.SessionApp
	RepositoryApp *application.RepositoryApp
	SSO           *application.SSOApp // sso_only
}

type InstanceResponse struct {
	Version             string `json:"version"`
	RegistrationEnabled bool   `json:"registration_enabled"`
	SSHEnabled          bool   `json:"ssh_enabled"`
	SSHPort             int    `json:"ssh_port,omitempty"`
	MaxAssetSize        int64  `json:"max_asset_size"` // bytes per release asset
	Passkeys            bool   `json:"passkeys"`       // security keys and passkey sign-in (needs http.public_url)
	// SSOOnly: sign in through providers (/auth/providers); password
	// sign-in is only for the configuration's break-glass accounts.
	SSOOnly bool `json:"sso_only"`
	// DirectorySignIn: the sign-in form also takes LDAP user names.
	DirectorySignIn bool `json:"directory_sign_in"`
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
	// Subgroups: a group's sub-groups the caller may see.
	Subgroups []Subgroup `json:"subgroups"`
}

type ResolveRequest struct {
	Path string `query:"path" json:"-" validate:"required"`
}

type ResolveResponse struct {
	Type string `json:"type"` // repository or group
	Path string `json:"path"`
	// From: the former path the request used (a moved group's); the UI
	// redirects it to path.
	From string `json:"from,omitempty"`
}

type Subgroup struct {
	Name        string `json:"name"` // its full path
	Description string `json:"description,omitempty"`
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
	fiberoapi.Get(grp, "/resolve", c.Resolve, fiberoapi.OpenAPIOptions{
		Summary: "What a page path below a sub-group is", OperationID: "site.resolve", Tags: tags, Security: "disabled",
		Description: "For the web UI: ?path=org/team/app/tree/main is the repository org/team/app, ?path=org/team/b the group org/team/b. Only what the caller may read; 404 otherwise.",
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
		MaxAssetSize:        c.Config.Releases.MaxAssetSize,
		Passkeys:            c.Config.Server.PublicURL != "",
	}
	if resp.MaxAssetSize <= 0 {
		resp.MaxAssetSize = application.DefaultMaxAssetSize
	}
	resp.DirectorySignIn = c.SSO.HasDirectory()
	if on, _, err := c.SSO.SSOOnly(); err == nil && on {
		resp.SSOOnly, resp.RegistrationEnabled = true, false
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
	name := req.Name
	if decoded, err := url.PathUnescape(name); err == nil {
		name = decoded // a sub-group's path: org%2Fteam
	}
	view, err := c.RepositoryApp.ViewNamespace(name, c.userId(ctx))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	subgroups := []Subgroup{}
	for _, g := range view.Subgroups {
		subgroups = append(subgroups, Subgroup{Name: g.Name, Description: g.Description})
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
		Subgroups:    subgroups,
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

func (c Controller) Resolve(ctx *fiber.Ctx, req ResolveRequest) (*ResolveResponse, *fiberoapi.ErrorResponse) {
	r, err := c.RepositoryApp.ResolvePath(req.Path, c.userId(ctx))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ResolveResponse{Type: r.Type, Path: r.Path, From: r.From}, nil
}
