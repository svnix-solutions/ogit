package user

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/user/dtos"
)

func (ctrl *Controller) GetProfile(ctx *fiber.Ctx, input struct{}) (*dtos.ProfileResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.user.get_profile").Logger()

	logger.Warn().Msg("GetProfile handler not implemented yet")

	return nil, nil
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
