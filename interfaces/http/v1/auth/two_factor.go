package auth

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
	"github.com/labbs/ogit/interfaces/http/websession"
)

// LoginTwoFactor completes a login that answered two_factor_required.
// Wrong codes count as failed logins for the account and the IP.
func (ctrl Controller) LoginTwoFactor(ctx *fiber.Ctx, req dtos.TwoFactorLoginRequest) (*dtos.LoginResponse, *fiberoapi.ErrorResponse) {
	ip := ctrl.Limits.ClientIP(ctx)
	user, err := ctrl.AuthApp.TwoFactor.ChallengeUser(req.Challenge)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	if r, blocked := ctrl.Limits.LoginBlocked(ctx.UserContext(), ip, user.Email); blocked {
		return nil, ratelimit.Refused(ctx, r)
	}
	resp, err := ctrl.AuthApp.CompleteTwoFactorLogin(req.Challenge, req.Code, key(req.Ceremony, req.Credential))
	if errors.Is(err, application.ErrInvalidCode) || errors.Is(err, application.ErrInvalidKey) {
		ctrl.Limits.LoginFailed(ctx.UserContext(), ip, user.Email)
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return resp, nil
}

// SessionTwoFactor is LoginTwoFactor for the web UI: the token goes into
// cookies.
func (ctrl Controller) SessionTwoFactor(ctx *fiber.Ctx, req dtos.TwoFactorLoginRequest) (*dtos.SessionResponse, *fiberoapi.ErrorResponse) {
	resp, errResp := ctrl.LoginTwoFactor(ctx, req)
	if errResp != nil {
		return nil, errResp
	}
	return ctrl.setSession(ctx, resp.Token)
}

func (ctrl Controller) setSession(ctx *fiber.Ctx, token string) (*dtos.SessionResponse, *fiberoapi.ErrorResponse) {
	expires := time.Now().Add(time.Duration(ctrl.Config.Session.ExpirationMinutes) * time.Minute)
	if err := websession.Set(ctx, token, expires, websession.Secure(ctx, ctrl.Config.Server.PublicURL)); err != nil {
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusInternalServerError, Details: "internal error", Type: "INTERNAL_ERROR"}
	}
	return &dtos.SessionResponse{ExpiresAt: expires}, nil
}

// Sudo confirms the session for sensitive actions (10 minutes).
func (ctrl Controller) Sudo(ctx *fiber.Ctx, req dtos.SudoRequest) (*dtos.SudoResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	user, err := ctrl.AuthApp.UserApp.GetById(auth.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	ip := ctrl.Limits.ClientIP(ctx)
	if r, blocked := ctrl.Limits.LoginBlocked(ctx.UserContext(), ip, user.Email); blocked {
		return nil, ratelimit.Refused(ctx, r)
	}
	until, err := ctrl.AuthApp.TwoFactor.Sudo(auth, req.Code, req.Password, key(req.Ceremony, req.Credential))
	if errors.Is(err, application.ErrInvalidCode) || errors.Is(err, application.ErrInvalidCredentials) || errors.Is(err, application.ErrInvalidKey) {
		ctrl.Limits.LoginFailed(ctx.UserContext(), ip, user.Email)
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.SudoResponse{Until: until.UTC().Format(time.RFC3339)}, nil
}
