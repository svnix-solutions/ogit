// Package releases serves /api/v1/repository/:identifier/releases. The
// routes take a session or a token (CI): optauth.Caller.
package releases

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
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
	Releases   *application.ReleaseApp
	PublicURL  string // http.public_url, for download links ("" : relative)
}

type Release struct {
	Id              string     `json:"id"`
	TagName         string     `json:"tag_name"`
	TargetCommitish string     `json:"target_commitish"`
	Name            string     `json:"name"`
	Body            string     `json:"body"`
	Draft           bool       `json:"draft"`
	Prerelease      bool       `json:"prerelease"`
	Author          string     `json:"author,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	PublishedAt     *time.Time `json:"published_at"`
	CanEdit         bool       `json:"can_edit"`
	Assets          []Asset    `json:"assets"`
}

type ListRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Offset     int    `query:"offset" json:"-"`
	Limit      int    `query:"limit" json:"-"`
}

type ListResponse struct {
	Releases []Release `json:"releases"`
}

type Fields struct {
	TagName         *string `json:"tag_name"`
	TargetCommitish *string `json:"target_commitish"` // branch or commit for a new tag; default branch when empty
	Name            *string `json:"name"`
	Body            *string `json:"body"`
	Draft           *bool   `json:"draft"`
	Prerelease      *bool   `json:"prerelease"`
}

type CreateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Fields
}

type IdRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	ReleaseId  string `path:"release_id" json:"-" validate:"required"`
}

type UpdateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	ReleaseId  string `path:"release_id" json:"-" validate:"required"`
	Fields
}

type RepoRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type TagRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Tag        string `query:"tag" json:"-" validate:"required"`
}

type NotesRequest struct {
	Identifier      string `path:"identifier" json:"-" validate:"required"`
	TagName         string `json:"tag_name" validate:"required"`
	TargetCommitish string `json:"target_commitish"`
	PreviousTagName string `json:"previous_tag_name"`
}

type NotesResponse struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Releases"}
	opts := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled",
			Description: desc + " Takes a session or a token (Authorization: token <value>); public repositories need neither for reading."}
	}
	fiberoapi.Get(repoGrp, "/:identifier/releases", c.List, opts("List releases", "releases.list", "Newest first; drafts only for writers."))
	fiberoapi.Post(repoGrp, "/:identifier/releases", c.Create, opts("Create a release", "releases.create",
		"Writers (developers and up, or a repository token with write:repository). tag_name: an existing tag, or a new one created from target_commitish when published. draft, prerelease."))
	fiberoapi.Get(repoGrp, "/:identifier/releases/latest", c.Latest, opts("Latest release", "releases.latest", "The newest published release that is not a pre-release."))
	fiberoapi.Get(repoGrp, "/:identifier/releases/by-tag", c.ByTag, opts("Release of a tag", "releases.by_tag", "?tag=<name>."))
	fiberoapi.Post(repoGrp, "/:identifier/releases/generate-notes", c.GenerateNotes, opts("Generate release notes", "releases.notes",
		"Writers. The pull requests merged since previous_tag_name (default: the latest release) up to tag_name (or target_commitish)."))
	fiberoapi.Get(repoGrp, "/:identifier/releases/:release_id", c.Get, opts("Get a release", "releases.get", ""))
	fiberoapi.Patch(repoGrp, "/:identifier/releases/:release_id", c.Update, opts("Edit or publish a release", "releases.update",
		"Writers. draft=false publishes a draft (creating its tag if needed); a published release keeps its tag."))
	fiberoapi.Delete(repoGrp, "/:identifier/releases/:release_id", c.Delete, opts("Delete a release", "releases.delete", "Writers. The tag stays; the assets go."))
	setupAssets(c, repoGrp, opts)
}

func (c Controller) caller(ctx *fiber.Ctx) (application.StatusCaller, *fiberoapi.ErrorResponse) {
	caller, err := optauth.Caller(ctx, c.SessionApp, c.TokenApp, c.Limits)
	var many *optauth.ErrTooManyFailures
	switch {
	case errors.As(err, &many):
		return caller, &fiberoapi.ErrorResponse{Code: fiber.StatusTooManyRequests, Details: err.Error(), Type: "RATE_LIMITED"}
	case err != nil:
		return caller, apierror.From(err, c.Logger)
	}
	return caller, nil
}

func (c Controller) toRelease(v *application.ReleaseView) Release {
	out := Release{Id: v.Id, TagName: v.TagName, TargetCommitish: v.TargetCommitish, Name: v.Name, Body: v.Body, Draft: v.Draft,
		Prerelease: v.Prerelease, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt, CanEdit: v.CanEdit, Assets: c.assets(v)}
	if v.Author != nil {
		out.Author = v.Author.Username
	}
	return out
}

func input(f Fields) application.ReleaseInput {
	return application.ReleaseInput{TagName: f.TagName, TargetCommitish: f.TargetCommitish, Name: f.Name, Body: f.Body, Draft: f.Draft, Prerelease: f.Prerelease}
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	views, err := c.Releases.List(id, path, caller, req.Offset, req.Limit)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ListResponse{Releases: make([]Release, 0, len(views))}
	for i := range views {
		resp.Releases = append(resp.Releases, c.toRelease(&views[i]))
	}
	return resp, nil
}

func (c Controller) one(ctx *fiber.Ctx, get func(application.StatusCaller) (*application.ReleaseView, error)) (*Release, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	v, err := get(caller)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := c.toRelease(v)
	return &out, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Release, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	return c.one(ctx, func(caller application.StatusCaller) (*application.ReleaseView, error) {
		if caller.UserId == "" {
			return nil, application.ErrForbidden
		}
		return c.Releases.Create(id, path, caller, input(req.Fields))
	})
}

func (c Controller) Latest(ctx *fiber.Ctx, req RepoRequest) (*Release, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	return c.one(ctx, func(caller application.StatusCaller) (*application.ReleaseView, error) {
		return c.Releases.Latest(id, path, caller)
	})
}

func (c Controller) ByTag(ctx *fiber.Ctx, req TagRequest) (*Release, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	return c.one(ctx, func(caller application.StatusCaller) (*application.ReleaseView, error) {
		return c.Releases.ByTag(id, path, caller, req.Tag)
	})
}

func (c Controller) Get(ctx *fiber.Ctx, req IdRequest) (*Release, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	return c.one(ctx, func(caller application.StatusCaller) (*application.ReleaseView, error) {
		return c.Releases.Get(id, path, caller, req.ReleaseId)
	})
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*Release, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	return c.one(ctx, func(caller application.StatusCaller) (*application.ReleaseView, error) {
		return c.Releases.Update(id, path, caller, req.ReleaseId, input(req.Fields))
	})
}

func (c Controller) Delete(ctx *fiber.Ctx, req IdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Releases.Delete(id, path, caller, req.ReleaseId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "release deleted"}, nil
}

func (c Controller) GenerateNotes(ctx *fiber.Ctx, req NotesRequest) (*NotesResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	notes, err := c.Releases.GenerateNotes(id, path, caller, req.TagName, req.TargetCommitish, req.PreviousTagName)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &NotesResponse{Name: notes.Name, Body: notes.Body}, nil
}
