package user

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/user/dtos"
)

func (ctrl *Controller) GetProfile(ctx *fiber.Ctx, input struct{}) (*dtos.ProfileResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.user.get_profile").Logger()

	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusUnauthorized,
			Details: "Authentication required",
			Type:    "AUTHENTICATION_REQUIRED",
		}
	}

	user, err := ctrl.UserApp.GetById(authCtx.UserID)
	if err != nil {
		logger.Error().Err(err).Str("user_id", authCtx.UserID).Msg("failed to get profile")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusNotFound,
			Details: "user not found",
			Type:    "USER_NOT_FOUND",
		}
	}

	return &dtos.ProfileResponse{
		Id:       user.Id,
		Username: user.Username,
		Email:    user.Email,
		Avatar:   user.AvatarUrl,
	}, nil
}

// CreateToken creates a personal access token for git over HTTP. Git uses
// the token name as username and the returned token as password.
func (ctrl *Controller) CreateToken(ctx *fiber.Ctx, req dtos.CreateTokenRequest) (*dtos.CreateTokenResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.user.create_token").Logger()

	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusUnauthorized,
			Details: "Authentication required",
			Type:    "AUTHENTICATION_REQUIRED",
		}
	}

	scopes := make([]domain.TokenScope, len(req.Scopes))
	for i, s := range req.Scopes {
		scopes[i] = domain.TokenScope(s)
	}

	plaintext, token, err := ctrl.TokenApp.CreateUserToken(authCtx.UserID, req.Name, req.Description, scopes, req.ExpiresAt)
	if err != nil {
		logger.Warn().Err(err).Str("user_id", authCtx.UserID).Msg("failed to create token")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Details: err.Error(),
			Type:    "CREATE_TOKEN_ERROR",
		}
	}

	return &dtos.CreateTokenResponse{
		Id:        token.Id,
		Name:      token.Name,
		Token:     plaintext,
		Scopes:    req.Scopes,
		ExpiresAt: token.ExpiresAt,
	}, nil
}

// ChangePassword replaces the caller's password. Their other sessions end.
func (ctrl *Controller) ChangePassword(ctx *fiber.Ctx, req dtos.ChangePasswordRequest) (*dtos.ChangePasswordResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	sessionId, _ := authCtx.Claims[application.SessionIDClaim].(string)
	if err := ctrl.AuthApp.ChangePassword(authCtx.UserID, sessionId, req.CurrentPassword, req.NewPassword); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.ChangePasswordResponse{Message: "password changed; other sessions have been ended"}, nil
}

func (ctrl *Controller) ListTokens(ctx *fiber.Ctx, _ struct{}) (*dtos.ListTokensResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	tokens, err := ctrl.TokenApp.ListUserTokens(authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListTokensResponse{Tokens: []dtos.TokenSummary{}}
	for _, t := range tokens {
		scopes := make([]string, len(t.Scopes))
		for i, s := range t.Scopes {
			scopes[i] = string(s)
		}
		resp.Tokens = append(resp.Tokens, dtos.TokenSummary{Id: t.Id, Name: t.Name, Description: t.Description, Scopes: scopes, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt})
	}
	return resp, nil
}

func (ctrl *Controller) RevokeToken(ctx *fiber.Ctx, req dtos.RevokeTokenRequest) (*dtos.RevokeTokenResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.TokenApp.RevokeUserToken(authCtx.UserID, req.TokenId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.RevokeTokenResponse{Message: "token revoked"}, nil
}
