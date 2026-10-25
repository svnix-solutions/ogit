// Package ghcompat answers the GitHub REST endpoints for commit statuses
// and releases under /api/v3 (and release uploads under /api/uploads), as
// GitHub Enterprise does, so CI plugins and release tools work against
// oGit unchanged. Nothing else pretends to be GitHub.
// Requests authenticate with a personal access token ("Authorization: token
// <pat>"); the web UI's cookie is not accepted here.
package ghcompat

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config     config.Config
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Limits     *ratelimit.Guard
	Statuses   *application.StatusApp
	Releases   *application.ReleaseApp
}

type ghUser struct {
	Login string `json:"login"`
	Id    string `json:"id"`
	Type  string `json:"type"`
}

type ghStatus struct {
	URL         string    `json:"url"`
	Id          string    `json:"id"`
	State       string    `json:"state"`
	Description *string   `json:"description"`
	TargetURL   *string   `json:"target_url"`
	Context     string    `json:"context"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Creator     *ghUser   `json:"creator"`
}

type ghCombined struct {
	State      string     `json:"state"`
	Statuses   []ghStatus `json:"statuses"`
	SHA        string     `json:"sha"`
	TotalCount int        `json:"total_count"`
	Repository struct {
		Id       string `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	} `json:"repository"`
	CommitURL string `json:"commit_url"`
	URL       string `json:"url"`
}

func Setup(c Controller, app *fiber.App) {
	g := app.Group("/api/v3/repos/:owner/:repo")
	g.Post("/statuses/:sha", c.create)
	g.Get("/statuses/:sha", c.list)
	// Refs may contain slashes: /commits/<ref>/status and /commits/<ref>/statuses.
	g.Get("/commits/*", c.commits)
	setupReleases(c, app)
}

func (c Controller) base() string {
	return strings.TrimRight(c.Config.Server.PublicURL, "/") + "/api/v3"
}

func (c Controller) fail(ctx *fiber.Ctx, code int, msg string) error {
	return ctx.Status(code).JSON(fiber.Map{"message": msg, "documentation_url": "https://docs.github.com/rest"})
}

func (c Controller) failWith(ctx *fiber.Ctx, err error) error {
	var many *optauth.ErrTooManyFailures
	if errors.As(err, &many) {
		return c.fail(ctx, fiber.StatusTooManyRequests, err.Error())
	}
	if errors.Is(err, application.ErrInvalidToken) {
		return c.fail(ctx, fiber.StatusUnauthorized, "Bad credentials")
	}
	e := apierror.From(err, c.Logger)
	msg := e.Details
	if e.Code == fiber.StatusNotFound {
		msg = "Not Found"
	}
	return c.fail(ctx, e.Code, msg)
}

func (c Controller) caller(ctx *fiber.Ctx) (application.StatusCaller, *string, error) {
	caller, err := optauth.Caller(ctx, c.SessionApp, c.TokenApp, c.Limits)
	path := ctx.Params("owner") + "/" + strings.TrimSuffix(ctx.Params("repo"), ".git")
	return caller, &path, err
}

func (c Controller) status(path string, s domain.CommitStatus) ghStatus {
	out := ghStatus{URL: c.base() + "/repos/" + path + "/statuses/" + s.SHA, Id: s.Id, State: string(s.State), Context: s.Context, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if s.Description != "" {
		out.Description = &s.Description
	}
	if s.TargetURL != "" {
		out.TargetURL = &s.TargetURL
	}
	if s.Creator != nil {
		out.Creator = &ghUser{Login: s.Creator.Username, Id: s.Creator.Id, Type: "User"}
	}
	return out
}

func (c Controller) create(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	if caller.UserId == "" {
		return c.fail(ctx, fiber.StatusUnauthorized, "Requires authentication")
	}
	var body struct {
		State       string `json:"state"`
		TargetURL   string `json:"target_url"`
		Description string `json:"description"`
		Context     string `json:"context"`
	}
	if err := ctx.BodyParser(&body); err != nil {
		return c.fail(ctx, fiber.StatusBadRequest, "Problems parsing JSON")
	}
	s, err := c.Statuses.Create(nil, path, caller, ctx.Params("sha"), application.StatusInput{State: body.State, Context: body.Context, Description: body.Description, TargetURL: body.TargetURL})
	if err != nil {
		if errors.Is(err, application.ErrInvalidStatus) || errors.Is(err, application.ErrCommitNotFound) {
			return c.fail(ctx, fiber.StatusUnprocessableEntity, "Validation Failed: "+err.Error())
		}
		return c.failWith(ctx, err)
	}
	return ctx.Status(fiber.StatusCreated).JSON(c.status(*path, *s))
}

func (c Controller) list(ctx *fiber.Ctx) error {
	return c.listRef(ctx, ctx.Params("sha"))
}

func (c Controller) listRef(ctx *fiber.Ctx, ref string) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	_, _, list, err := c.Statuses.List(nil, path, caller, ref)
	if err != nil {
		return c.failWith(ctx, err)
	}
	out := make([]ghStatus, 0, len(list))
	for _, s := range list {
		out = append(out, c.status(*path, s))
	}
	return ctx.JSON(out)
}

func (c Controller) commits(ctx *fiber.Ctx) error {
	rest := ctx.Params("*")
	if ref, ok := strings.CutSuffix(rest, "/statuses"); ok {
		return c.listRef(ctx, ref)
	}
	ref, ok := strings.CutSuffix(rest, "/status")
	if !ok || ref == "" {
		return c.fail(ctx, fiber.StatusNotFound, "Not Found")
	}
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	repo, comb, err := c.Statuses.CombinedFor(nil, path, caller, ref)
	if err != nil {
		return c.failWith(ctx, err)
	}
	out := ghCombined{State: string(comb.State), SHA: comb.SHA, TotalCount: len(comb.Statuses), Statuses: make([]ghStatus, 0, len(comb.Statuses))}
	for _, s := range comb.Statuses {
		out.Statuses = append(out.Statuses, c.status(*path, s))
	}
	out.Repository.Id, out.Repository.Name, out.Repository.FullName, out.Repository.Private = repo.Id, repo.Slug, repo.FullPath(), repo.IsPrivate
	out.CommitURL = c.base() + "/repos/" + *path + "/commits/" + comb.SHA
	out.URL = out.CommitURL + "/status"
	return ctx.JSON(out)
}
