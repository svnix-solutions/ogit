package repository

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type RepoToken struct {
	Id          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
	// Token is the value, only in the answer to its creation.
	Token string `json:"token,omitempty"`
}

type ListRepoTokensRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ListRepoTokensResponse struct {
	Tokens []RepoToken `json:"tokens"`
}

type CreateRepoTokenRequest struct {
	Identifier  string     `path:"identifier" json:"-" validate:"required"`
	Name        string     `json:"name" validate:"required"`
	Description string     `json:"description"`
	Scopes      []string   `json:"scopes"` // read:repository, write:repository, write:status
	ExpiresAt   *time.Time `json:"expires_at"`
}

type RepoTokenRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	TokenId    string `path:"token_id" json:"-" validate:"required"`
}

type RepoTokenMessage struct {
	Message string `json:"message"`
}

func setupTokenRoutes(controller Controller) {
	tags := []string{"Repository tokens"}
	fiberoapi.Get(controller.FiberOapi, "/:identifier/tokens", controller.ListRepoTokens, fiberoapi.OpenAPIOptions{
		Summary: "List repository tokens", Description: "Maintainers and owners. Values are never shown again.", OperationID: "repository.tokens.list", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/tokens", controller.CreateRepoToken, fiberoapi.OpenAPIOptions{
		Summary: "Create a repository token",
		Description: "Maintainers and owners. A token bound to this repository, for CI: read:repository (clone, fetch, read this repository's API), " +
			"write:repository (push, under branch protection), write:status (commit statuses). Use it as the password of git's HTTP auth, " +
			"or \"Authorization: token <value>\" on the API. The value is in this answer only.",
		OperationID: "repository.tokens.create", Tags: tags,
	})
	fiberoapi.Delete(controller.FiberOapi, "/:identifier/tokens/:token_id", controller.RevokeRepoToken, fiberoapi.OpenAPIOptions{
		Summary: "Revoke a repository token", Description: "Maintainers and owners.", OperationID: "repository.tokens.revoke", Tags: tags,
	})
}

func repoToken(t *domain.Token) RepoToken {
	scopes := make([]string, 0, len(t.Scopes))
	for _, s := range t.Scopes {
		scopes = append(scopes, string(s))
	}
	return RepoToken{Id: t.Id, Name: t.Name, Description: t.Description, Scopes: scopes, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt}
}

func (ctrl *Controller) ListRepoTokens(ctx *fiber.Ctx, req ListRepoTokensRequest) (*ListRepoTokensResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	tokens, err := ctrl.RepoTokens.List(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &ListRepoTokensResponse{Tokens: make([]RepoToken, 0, len(tokens))}
	for i := range tokens {
		resp.Tokens = append(resp.Tokens, repoToken(&tokens[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateRepoToken(ctx *fiber.Ctx, req CreateRepoTokenRequest) (*RepoToken, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	scopes := make([]domain.TokenScope, 0, len(req.Scopes))
	for _, s := range req.Scopes {
		scopes = append(scopes, domain.TokenScope(s))
	}
	id, path := repoid.Split(req.Identifier)
	value, token, err := ctrl.RepoTokens.Create(id, path, authCtx.UserID, req.Name, req.Description, scopes, req.ExpiresAt)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := repoToken(token)
	out.Token = value
	return &out, nil
}

func (ctrl *Controller) RevokeRepoToken(ctx *fiber.Ctx, req RepoTokenRequest) (*RepoTokenMessage, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.RepoTokens.Revoke(id, path, authCtx.UserID, req.TokenId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &RepoTokenMessage{Message: "token revoked"}, nil
}
