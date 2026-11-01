// Package workspaces serves a user's workspaces: /api/v1/user/workspaces and
// /api/v1/repository/:identifier/workspaces.
package workspaces

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	Workspaces *application.WorkspaceApp
}

type Workspace struct {
	Id         string    `json:"id"`
	Repository string    `json:"repository"` // empty when the caller can't read it any more
	Branch     string    `json:"branch"`
	State      string    `json:"state"` // stopped, starting, running or stopping
	LastUsedAt time.Time `json:"last_used_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type ListResponse struct {
	Workspaces []Workspace `json:"workspaces"`
}

type ListMineRequest struct{}

type RepositoryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type CreateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Branch     string `json:"branch"` // the default branch when empty
}

type IdRequest struct {
	WorkspaceId string `path:"workspace_id" json:"-" validate:"required"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, userGrp, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Workspaces"}
	options := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	fiberoapi.Get(userGrp, "/workspaces", c.ListMine, options("List your workspaces", "workspaces.list", "All of yours, the most recently used first."))
	fiberoapi.Get(userGrp, "/workspaces/:workspace_id", c.Get, options("Get a workspace", "workspaces.get", "Yours only."))
	fiberoapi.Post(userGrp, "/workspaces/:workspace_id/start", c.Start, options("Start a workspace", "workspaces.start", "Needs write access to its repository. Refused over workspaces.max_running."))
	fiberoapi.Post(userGrp, "/workspaces/:workspace_id/stop", c.Stop, options("Stop a workspace", "workspaces.stop", "Yours only."))
	fiberoapi.Delete(userGrp, "/workspaces/:workspace_id", c.Delete, options("Delete a workspace", "workspaces.delete", "Yours only, once stopped."))
	fiberoapi.Get(repoGrp, "/:identifier/workspaces", c.ListRepository, options("List your workspaces on a repository", "workspaces.repository", "Yours only."))
	fiberoapi.Post(repoGrp, "/:identifier/workspaces", c.Create, options("Create a workspace", "workspaces.create", "Developers and up. branch: the default branch when empty. It starts stopped."))
}

func toWorkspace(v application.WorkspaceView) Workspace {
	return Workspace{Id: v.Id, Repository: v.Repository, Branch: v.Branch, State: string(v.State), LastUsedAt: v.LastUsedAt, CreatedAt: v.CreatedAt}
}

func toList(views []application.WorkspaceView) *ListResponse {
	resp := &ListResponse{Workspaces: make([]Workspace, 0, len(views))}
	for _, v := range views {
		resp.Workspaces = append(resp.Workspaces, toWorkspace(v))
	}
	return resp
}

func authed(ctx *fiber.Ctx) (string, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", apierror.Unauthenticated()
	}
	return auth.UserID, nil
}

func (c Controller) ListMine(ctx *fiber.Ctx, _ ListMineRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	views, err := c.Workspaces.List(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return toList(views), nil
}

func (c Controller) ListRepository(ctx *fiber.Ctx, req RepositoryRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	views, err := c.Workspaces.ListForRepository(id, path, user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return toList(views), nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Workspace, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Workspaces.Create(id, path, user, req.Branch)
	return c.answer(v, err)
}

func (c Controller) Get(ctx *fiber.Ctx, req IdRequest) (*Workspace, *fiberoapi.ErrorResponse) {
	return c.act(ctx, req, c.Workspaces.Get)
}

func (c Controller) Start(ctx *fiber.Ctx, req IdRequest) (*Workspace, *fiberoapi.ErrorResponse) {
	return c.act(ctx, req, c.Workspaces.Start)
}

func (c Controller) Stop(ctx *fiber.Ctx, req IdRequest) (*Workspace, *fiberoapi.ErrorResponse) {
	return c.act(ctx, req, c.Workspaces.Stop)
}

func (c Controller) act(ctx *fiber.Ctx, req IdRequest, do func(userId, id string) (*application.WorkspaceView, error)) (*Workspace, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	v, err := do(user, req.WorkspaceId)
	return c.answer(v, err)
}

func (c Controller) answer(v *application.WorkspaceView, err error) (*Workspace, *fiberoapi.ErrorResponse) {
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := toWorkspace(*v)
	return &out, nil
}

func (c Controller) Delete(ctx *fiber.Ctx, req IdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	if err := c.Workspaces.Delete(user, req.WorkspaceId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "workspace deleted"}, nil
}
