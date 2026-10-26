// Package packages serves /api/v1/packages (a namespace's packages) and
// /api/v1/repository/:identifier/packages (rules in application/packages.go).
package packages

import (
	"cmp"
	"encoding/json"
	"net/url"
	"slices"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Limits     *ratelimit.Guard
	Packages   *application.PackagesApp
	Browse     *application.BrowseApp
	GoProxy    *application.GoProxyApp
}

type Package struct {
	Id         string    `json:"id"`
	Namespace  string    `json:"namespace"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	Visibility string    `json:"visibility"` // repository, public or private
	Public     bool      `json:"public"`
	Repository *string   `json:"repository"` // <namespace>/<slug>, when linked and readable
	CanWrite   bool      `json:"can_write"`
	CanAdmin   bool      `json:"can_admin"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type File struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Sha256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
}

type Version struct {
	Version   string         `json:"version"` // a digest for container images
	Size      int64          `json:"size"`
	Metadata  map[string]any `json:"metadata"`
	Publisher *string        `json:"publisher"` // username, or the token's name
	Files     []File         `json:"files"`
	Tags      []string       `json:"tags"`
	CreatedAt time.Time      `json:"created_at"`
}

type ListRequest struct {
	Namespace string `path:"namespace" json:"-" validate:"required"`
	Type      string `query:"type" json:"-"`
}

type ListResponse struct {
	Packages []Package `json:"packages"`
}

type RepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type PackageRequest struct {
	Namespace string `path:"namespace" json:"-" validate:"required"`
	Type      string `path:"type" json:"-" validate:"required"`
	Name      string `path:"name" json:"-" validate:"required"` // image names with slashes URL-encoded
}

type DetailResponse struct {
	Package  Package   `json:"package"`
	Versions []Version `json:"versions"`
	Readme   string    `json:"readme"` // npm: the latest version's
}

type GoVersion struct {
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
}

type GoModuleResponse struct {
	Path     string                  `json:"path"`
	Versions []GoVersion             `json:"versions"`
	Requires []gitbrowse.Requirement `json:"requires"`
}

type UpdateRequest struct {
	Namespace  string  `path:"namespace" json:"-" validate:"required"`
	Type       string  `path:"type" json:"-" validate:"required"`
	Name       string  `path:"name" json:"-" validate:"required"`
	Visibility *string `json:"visibility"`
	Repository *string `json:"repository"` // <namespace>/<slug>, "" to unlink
}

type DeleteRequest struct {
	Namespace string `path:"namespace" json:"-" validate:"required"`
	Type      string `path:"type" json:"-" validate:"required"`
	Name      string `path:"name" json:"-" validate:"required"`
	Confirm   string `query:"confirm" json:"-"` // the package's name
}

type VersionRequest struct {
	Namespace string `path:"namespace" json:"-" validate:"required"`
	Type      string `path:"type" json:"-" validate:"required"`
	Name      string `path:"name" json:"-" validate:"required"`
	Version   string `path:"version" json:"-" validate:"required"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, grp, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Packages"}
	opts := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		// Sessions or personal access tokens (read:packages, write:packages).
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled", Description: desc}
	}
	fiberoapi.Get(grp, "/packages/:namespace", c.List, opts("List a namespace's packages", "packages.list", "The packages of a user or group the caller can read; ?type=container|npm|go|generic."))
	fiberoapi.Get(grp, "/packages/:namespace/:type/:name", c.Get, opts("Get a package", "packages.get", "With its versions (files, or tags for container images)."))
	fiberoapi.Patch(grp, "/packages/:namespace/:type/:name", c.Update, opts("Change a package's visibility or repository", "packages.update", "Package admins. visibility: repository (follows the linked repository), public or private; repository: <namespace>/<slug> of the same namespace, \"\" to unlink."))
	fiberoapi.Delete(grp, "/packages/:namespace/:type/:name", c.Delete, opts("Delete a package", "packages.delete", "Package admins, with ?confirm=<name>. Every version goes."))
	fiberoapi.Delete(grp, "/packages/:namespace/:type/:name/versions/:version", c.DeleteVersion, opts("Delete a package version", "packages.versions.delete", "Package admins. For container images the version is the manifest's digest; its tags go with it."))
	fiberoapi.Get(repoGrp, "/:identifier/packages", c.ForRepository, opts("List a repository's packages", "packages.repository", "The packages linked to the repository."))
	fiberoapi.Get(repoGrp, "/:identifier/go-module", c.GoModule, opts("Get a repository's Go module", "packages.gomodule",
		"The module the repository serves through the Go proxy (its default branch's go.mod names <host>/<namespace>/<slug>[/vN]): versions from its tags, and the latest version's requirements. 404 GO_MODULE_NOT_FOUND otherwise."))
}

func (c Controller) caller(ctx *fiber.Ctx) (application.PackageCaller, *fiberoapi.ErrorResponse) {
	caller, err := optauth.Caller(ctx, c.SessionApp, c.TokenApp, c.Limits)
	if err != nil {
		if tm, ok := err.(*optauth.ErrTooManyFailures); ok {
			return caller, &fiberoapi.ErrorResponse{Code: fiber.StatusTooManyRequests, Details: tm.Error(), Type: "TOO_MANY_REQUESTS"}
		}
		return caller, apierror.From(err, c.Logger)
	}
	return caller, nil
}

func signedIn(ctx *fiber.Ctx, c Controller) (application.PackageCaller, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e == nil && caller.UserId == "" {
		e = apierror.Unauthenticated()
	}
	return caller, e
}

func name(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func toPackage(v application.PackageView) Package {
	out := Package{Id: v.Id, Namespace: v.Namespace, Type: v.Type, Name: v.Name, Visibility: v.Visibility, Public: v.Public,
		CanWrite: v.CanWrite, CanAdmin: v.CanAdmin, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if v.Repository != nil {
		path := v.Repository.Namespace + "/" + v.Repository.Slug
		out.Repository = &path
	}
	return out
}

func (c Controller) toVersion(v application.VersionView) Version {
	out := Version{Version: v.Version, Size: v.Size, Tags: v.Tags, Files: make([]File, 0, len(v.Files)), CreatedAt: v.CreatedAt, Metadata: map[string]any{}}
	_ = json.Unmarshal([]byte(v.Metadata), &out.Metadata)
	delete(out.Metadata, "readme") // npm: one README per response, in DetailResponse
	if v.Publisher != "" {
		out.Publisher = &v.Publisher
	}
	for _, f := range v.Files {
		out.Files = append(out.Files, File{Name: f.Name, Size: f.Size, Sha256: f.Sha256, CreatedAt: f.CreatedAt})
	}
	return out
}

func list(views []application.PackageView) *ListResponse {
	out := &ListResponse{Packages: make([]Package, 0, len(views))}
	for _, v := range views {
		out.Packages = append(out.Packages, toPackage(v))
	}
	return out
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	views, err := c.Packages.List(req.Namespace, req.Type, caller)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return list(views), nil
}

func (c Controller) ForRepository(ctx *fiber.Ctx, req RepositoryRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	views, err := c.Packages.ForRepository(id, path, caller)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return list(views), nil
}

func (c Controller) Get(ctx *fiber.Ctx, req PackageRequest) (*DetailResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	p, versions, err := c.Packages.Get(req.Namespace, req.Type, name(req.Name), caller)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := &DetailResponse{Package: toPackage(*p), Versions: make([]Version, 0, len(versions))}
	latest, newest := "", "" // READMEs of the latest-tagged and the newest version
	for i, v := range versions {
		var meta struct {
			Readme string `json:"readme"`
		}
		_ = json.Unmarshal([]byte(v.Metadata), &meta)
		if slices.Contains(v.Tags, "latest") {
			latest = meta.Readme
		} else if i == 0 {
			newest = meta.Readme
		}
		out.Versions = append(out.Versions, c.toVersion(v))
	}
	out.Readme = cmp.Or(latest, newest)
	return out, nil
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*Package, *fiberoapi.ErrorResponse) {
	caller, e := signedIn(ctx, c)
	if e != nil {
		return nil, e
	}
	v, err := c.Packages.Update(req.Namespace, req.Type, name(req.Name), caller, application.PackageChange{Visibility: req.Visibility, Repository: req.Repository})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := toPackage(*v)
	return &out, nil
}

func (c Controller) Delete(ctx *fiber.Ctx, req DeleteRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	caller, e := signedIn(ctx, c)
	if e != nil {
		return nil, e
	}
	if req.Confirm != name(req.Name) {
		return nil, apierror.From(application.ErrConfirmMismatch, c.Logger)
	}
	if err := c.Packages.Delete(req.Namespace, req.Type, name(req.Name), caller); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "package deleted"}, nil
}

func (c Controller) DeleteVersion(ctx *fiber.Ctx, req VersionRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	caller, e := signedIn(ctx, c)
	if e != nil {
		return nil, e
	}
	if err := c.Packages.DeleteVersion(req.Namespace, req.Type, name(req.Name), name(req.Version), caller); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "version deleted"}, nil
}

type GoModuleRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Summary    bool   `query:"summary" json:"-"` // the path only (cheap: no tags read)
}

func (c Controller) GoModule(ctx *fiber.Ctx, req GoModuleRequest) (*GoModuleResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	repo, st, err := c.Browse.Open(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	m, err := c.GoProxy.RepoModule(st, repo.FullPath(), ctx.Hostname(), req.Summary)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := &GoModuleResponse{Path: m.Path, Versions: make([]GoVersion, 0, len(m.Versions)), Requires: m.Requires}
	if out.Requires == nil {
		out.Requires = []gitbrowse.Requirement{}
	}
	for _, v := range m.Versions {
		out.Versions = append(out.Versions, GoVersion{Version: v.Version, Time: v.Time})
	}
	return out, nil
}
