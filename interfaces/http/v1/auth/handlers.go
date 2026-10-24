package auth

import (
	"errors"
	"github.com/labbs/ogit/infrastructure/ratelimit"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
)

func (ctrl Controller) Login(ctx *fiber.Ctx, req dtos.LoginRequest) (*dtos.LoginResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.auth.login").Logger()

	// Failed attempts are limited per IP and per account (any IP).
	ip := ctrl.Limits.ClientIP(ctx)
	if r, blocked := ctrl.Limits.LoginBlocked(ctx.UserContext(), ip, req.Email); blocked {
		return nil, ratelimit.Refused(ctx, r)
	}
	resp, err := ctrl.AuthApp.Authenticate(req.Email, req.Password, ctx)
	switch {
	case err == nil:
		return resp, nil
	case errors.Is(err, application.ErrInvalidCredentials):
		ctrl.Limits.LoginFailed(ctx.UserContext(), ip, req.Email)
		// Same answer for an unknown email and a wrong password.
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusUnauthorized, Details: err.Error(), Type: "INVALID_CREDENTIALS"}
	case errors.Is(err, application.ErrAccountDisabled):
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusForbidden, Details: err.Error(), Type: "ACCOUNT_DISABLED"}
	case errors.Is(err, application.ErrEmailNotVerified):
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusForbidden,
			Details: "verify your email address first; POST /api/v1/auth/verify/resend sends a new link",
			Type:    "EMAIL_NOT_VERIFIED",
		}
	default:
		logger.Error().Err(err).Msg("failed to authenticate user")
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusInternalServerError, Details: "internal error", Type: "INTERNAL_ERROR"}
	}
}

func (ctrl Controller) Logout(ctx *fiber.Ctx, input struct{}) (*dtos.LogoutResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.auth.logout").Logger()

	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusUnauthorized,
			Details: "Authentication required",
			Type:    "AUTHENTICATION_REQUIRED",
		}
	}

	sessionId, _ := authCtx.Claims[application.SessionIDClaim].(string)
	if sessionId == "" {
		logger.Error().Str("user_id", authCtx.UserID).Msg("auth context has no session id")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Details: "session not found",
			Type:    "LOGOUT_FAILED",
		}
	}

	if err := ctrl.AuthApp.Logout(sessionId); err != nil {
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Details: "failed to end session",
			Type:    "LOGOUT_FAILED",
		}
	}

	return &dtos.LogoutResponse{
		Message: "Logged out",
	}, nil
}

func (ctrl Controller) Register(ctx *fiber.Ctx, req dtos.RegisterRequest) (*dtos.RegisterResponse, *fiberoapi.ErrorResponse) {
	requestId := ctx.Locals("requestid").(string)
	logger := ctrl.Logger.With().Str("request_id", requestId).Str("component", "http.api.v1.auth.register").Logger()

	if r := ctrl.Limits.Allow(ctx.UserContext(), ctrl.Limits.Rules.RegisterIP, "register:ip:"+ctrl.Limits.ClientIP(ctx)); !r.Allowed {
		return nil, ratelimit.Refused(ctx, r)
	}
	err := ctrl.AuthApp.Register(req.Username, req.Email, req.Password)
	switch {
	case err == nil:
	case errors.Is(err, application.ErrRegistrationDisabled):
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusForbidden,
			Details: err.Error(),
			Type:    "REGISTRATION_DISABLED",
		}
	case errors.Is(err, application.ErrRegistrationRejected):
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Details: err.Error(),
			Type:    "REGISTRATION_REJECTED",
		}
	default:
		logger.Error().Err(err).Str("email", req.Email).Msg("failed to register user")
		return nil, &fiberoapi.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
			Type:    "REGISTRATION_FAILED",
		}
	}

	return &dtos.RegisterResponse{
		Message: "User registered successfully",
	}, nil
}
