// Package wiki serves repositories' wikis (rules in application/wiki_app.go).
// Reads need no session on public repositories; history, diffs and files
// are the browse and raw routes on <path>.wiki.
package wiki

import (
	"bytes"
	"io"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Wikis      *application.WikiApp
}

type InfoRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type PageRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `query:"name" json:"-"`    // Home when empty
	Version    string `query:"version" json:"-"` // a commit hash: the page as it was then
}

type SaveRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `json:"name" validate:"required"`
	NewName    string `json:"new_name"` // renames the page
	Content    string `json:"content"`
	Message    string `json:"message"`
	// Base: the head the edit started from (GET page's head); a page
	// changed since is a conflict. Empty overwrites.
	Base string `json:"base"`
}

type DeleteRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `query:"name" json:"-" validate:"required"`
	Base       string `query:"base" json:"-"`
}

type RevertRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `json:"name" validate:"required"`
	Version    string `json:"version" validate:"required"` // the commit to go back to
	Base       string `json:"base"`
}

type SearchRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Query      string `query:"q" json:"-"`
}

type SearchResponse struct {
	Results []application.WikiSearchResult `json:"results"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup, app *fiber.App) {
	tags := []string{"Wiki"}
	public := func(summary, id, description string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled",
			Description: description + " No session needed for public repositories."}
	}
	opts := func(summary, id, description string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: description}
	}
	fiberoapi.Get(repoGrp, "/:identifier/wiki", c.Info, public("Wiki", "wiki.info",
		"Who edits it, whether it exists yet, whether the caller can edit, and its pages. History and diffs: the browse routes on <path>.wiki (/repository/<owner>%2F<repo>.wiki/commits?spec=<branch>/<file>); files: /<owner>/<repo>.wiki/raw/<ref>/<path>; git: <path>.wiki.git."))
	fiberoapi.Get(repoGrp, "/:identifier/wiki/page", c.Page, public("Wiki page", "wiki.page",
		"?name= (Home when empty), &version=<commit>: the page with the nearest sidebar and footer, and the wiki's head (the base of an edit)."))
	fiberoapi.Get(repoGrp, "/:identifier/wiki/search", c.Search, public("Search the wiki", "wiki.search", "?q=: pages containing every word, in titles or content."))
	fiberoapi.Post(repoGrp, "/:identifier/wiki/pages", c.Create, opts("Create a wiki page", "wiki.create", "Creates a page (409 if it exists)."))
	fiberoapi.Put(repoGrp, "/:identifier/wiki/page", c.Update, opts("Edit a wiki page", "wiki.update", "Edits or renames a page; base is the head it was read at."))
	fiberoapi.Delete(repoGrp, "/:identifier/wiki/page", c.Delete, opts("Delete a wiki page", "wiki.delete", "?name=&base="))
	fiberoapi.Post(repoGrp, "/:identifier/wiki/revert", c.Revert, opts("Revert a wiki page", "wiki.revert", "Writes the page as it was at version, as a new commit."))
	// Attachments are files, not JSON: a plain route, the file as the
	// body, ?name=<file name> (streamed; wiki attachments' size limit).
	app.Post("/api/v1/repository/:identifier/wiki/uploads", c.Upload)
}

func (c Controller) reader(ctx *fiber.Ctx) string {
	return optauth.Reader(ctx, c.SessionApp, c.TokenApp)
}

func (c Controller) Info(ctx *fiber.Ctx, req InfoRequest) (*application.WikiInfo, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	info, err := c.Wikis.Info(id, path, c.reader(ctx))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return info, nil
}

func (c Controller) Page(ctx *fiber.Ctx, req PageRequest) (*application.WikiView, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	view, err := c.Wikis.Page(id, path, c.reader(ctx), req.Name, req.Version)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return view, nil
}

func (c Controller) Search(ctx *fiber.Ctx, req SearchRequest) (*SearchResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	results, err := c.Wikis.Search(id, path, c.reader(ctx), req.Query)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &SearchResponse{Results: results}, nil
}

func (c Controller) save(ctx *fiber.Ctx, identifier string, e application.WikiEdit) (*application.WikiChange, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(identifier)
	change, err := c.Wikis.Save(id, path, authCtx.UserID, e)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return change, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req SaveRequest) (*application.WikiChange, *fiberoapi.ErrorResponse) {
	return c.save(ctx, req.Identifier, application.WikiEdit{Name: req.Name, Content: req.Content, Message: req.Message, New: true})
}

func (c Controller) Update(ctx *fiber.Ctx, req SaveRequest) (*application.WikiChange, *fiberoapi.ErrorResponse) {
	return c.save(ctx, req.Identifier, application.WikiEdit{Name: req.Name, NewName: req.NewName, Content: req.Content, Message: req.Message, Base: req.Base})
}

func (c Controller) Delete(ctx *fiber.Ctx, req DeleteRequest) (*application.WikiChange, *fiberoapi.ErrorResponse) {
	return c.save(ctx, req.Identifier, application.WikiEdit{Name: req.Name, Base: req.Base, Delete: true})
}

func (c Controller) Revert(ctx *fiber.Ctx, req RevertRequest) (*application.WikiChange, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	change, err := c.Wikis.Revert(id, path, authCtx.UserID, req.Name, req.Version, req.Base)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return change, nil
}

func (c Controller) Upload(ctx *fiber.Ctx) error {
	userId := optauth.UserID(ctx, c.SessionApp)
	if userId == "" {
		e := apierror.Unauthenticated()
		return ctx.Status(e.Code).JSON(e)
	}
	var body io.Reader = bytes.NewReader(ctx.Body())
	if ctx.Request().IsBodyStream() {
		body = ctx.Request().BodyStream()
	}
	id, path := repoid.Split(ctx.Params("identifier"))
	a, err := c.Wikis.Attach(id, path, userId, ctx.Query("name"), body)
	if err != nil {
		e := apierror.From(err, c.Logger)
		return ctx.Status(e.Code).JSON(e)
	}
	return ctx.JSON(a)
}
