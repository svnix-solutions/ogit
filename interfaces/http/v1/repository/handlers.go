package repository

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/helpers/mapper"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

func (ctrl *Controller) GetRepository(ctx *fiber.Ctx, req dtos.GetRepositoryRequest) (*dtos.GetRepositoryResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.repository.get").Logger()

	id, path := repoid.Split(req.Identifier)

	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusUnauthorized,
			Details: "Authentication required",
			Type:    "AUTHENTICATION_REQUIRED",
		}
	}

	repo, err := ctrl.RepositoryApp.GetRepositoryForUser(id, path, authCtx.UserID)
	if errors.Is(err, application.ErrRepositoryNotFound) {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusNotFound,
			Details: err.Error(),
			Type:    "REPOSITORY_NOT_FOUND",
		}
	} else if err != nil {
		logger.Error().Err(err).Str("identifier", req.Identifier).Msg("failed to get repository")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
			Type:    "GET_REPOSITORY_ERROR",
		}
	}

	var response dtos.GetRepositoryResponse
	mapper.MapStructByFieldNames(repo, &response)
	response.FullPath = repo.FullPath()
	role, err := ctrl.RepositoryApp.GetUserEffectiveRole(repo.Id, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, logger)
	}
	response.Role = string(role)

	return &response, nil
}

func (ctrl *Controller) CreateRepository(ctx *fiber.Ctx, req dtos.CreateRepositoryRequest) (*dtos.CreateRepositoryResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.repository.create").Logger()

	// Get the authenticated user context
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("failed to get auth context")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusUnauthorized,
			Details: "Authentication required",
			Type:    "AUTHENTICATION_REQUIRED",
		}
	}

	isPrivate := true
	if req.IsPrivate != nil {
		isPrivate = *req.IsPrivate
	}

	repo, err := ctrl.RepositoryApp.Create(req.Namespace, req.Name, req.Description, authCtx.UserID, isPrivate)
	if err != nil {
		return nil, apierror.From(err, logger)
	}

	var response dtos.CreateRepositoryResponse
	mapper.MapStructByFieldNames(repo, &response)
	response.FullPath = repo.FullPath()

	return &response, nil
}
