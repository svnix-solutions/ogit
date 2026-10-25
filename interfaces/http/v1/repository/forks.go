package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

func setupForkRoutes(controller Controller) {
	tags := []string{"Forks"}
	fiberoapi.Post(controller.FiberOapi, "/:identifier/forks", controller.Fork, fiberoapi.OpenAPIOptions{
		Summary: "Fork a repository",
		Description: "Anyone who can read it, into their namespace or a group they maintain. A full copy of its branches and tags; " +
			"forks of private repositories are private.",
		OperationID: "repository.fork", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/forks", controller.ListForks, fiberoapi.OpenAPIOptions{
		Summary: "List forks", Description: "The forks the caller can read. No session needed for public ones.",
		OperationID: "repository.forks", Tags: tags, Security: "disabled",
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/sync", controller.SyncFork, fiberoapi.OpenAPIOptions{
		Summary: "Sync a fork's branch", Description: "Writers of the fork: fast-forwards a branch to the original's. Refused if the branch has its own commits.",
		OperationID: "repository.sync", Tags: tags,
	})
}

func (ctrl *Controller) Fork(ctx *fiber.Ctx, req dtos.ForkRequest) (*dtos.GetRepositoryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	fork, err := ctrl.RepositoryApp.Fork(id, path, authCtx.UserID, req.Namespace, req.Name)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := repositoryResponse(fork)
	resp.IsFork = true
	if parent := ctrl.RepositoryApp.Parent(fork, authCtx.UserID); parent != nil {
		resp.ForkOf = &dtos.RepositoryRef{Id: parent.Id, FullPath: parent.FullPath()}
	}
	return resp, nil
}

func (ctrl *Controller) ListForks(ctx *fiber.Ctx, req dtos.ListForksRequest) (*dtos.ListForksResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	forks, err := ctrl.RepositoryApp.Forks(id, path, optauth.UserID(ctx, ctrl.SessionApp))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListForksResponse{Forks: make([]dtos.RepositorySummary, 0, len(forks))}
	for _, r := range forks {
		resp.Forks = append(resp.Forks, dtos.RepositorySummary{Id: r.Id, Namespace: r.Namespace, Slug: r.Slug, FullPath: r.FullPath(),
			Name: r.Name, Description: r.Description, IsPrivate: r.IsPrivate, IsArchived: r.IsArchived})
	}
	return resp, nil
}

func (ctrl *Controller) SyncFork(ctx *fiber.Ctx, req dtos.SyncRequest) (*dtos.SyncResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	res, err := ctrl.RepositoryApp.Sync(id, path, authCtx.UserID, req.Branch)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.SyncResponse{Branch: res.Branch, Before: res.Before, After: res.After, Updated: res.Updated}, nil
}
