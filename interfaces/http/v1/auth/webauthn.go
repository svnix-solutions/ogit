package auth

import (
	"encoding/json"
	"errors"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
)

// KeyOptions converts a ceremony for the client.
func KeyOptions(opts *application.KeyOptions, err error) (*dtos.KeyOptionsResponse, error) {
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(opts.Options)
	if err != nil {
		return nil, err
	}
	return &dtos.KeyOptionsResponse{Ceremony: opts.Ceremony, Options: raw}, nil
}

// key reads a security key's answer from a request; nil without one.
func key(ceremony string, credential json.RawMessage) *application.KeyAssertion {
	if ceremony == "" || len(credential) == 0 || string(credential) == "null" {
		return nil
	}
	return &application.KeyAssertion{Ceremony: ceremony, Response: credential}
}

// LoginKeyOptions starts the security key check of a login that answered
// two_factor_required.
func (ctrl Controller) LoginKeyOptions(ctx *fiber.Ctx, req dtos.ChallengeRequest) (*dtos.KeyOptionsResponse, *fiberoapi.ErrorResponse) {
	resp, err := KeyOptions(ctrl.AuthApp.TwoFactor.BeginLoginKey(req.Challenge))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return resp, nil
}

// SudoKeyOptions starts a security key check for /auth/sudo.
func (ctrl Controller) SudoKeyOptions(ctx *fiber.Ctx, _ struct{}) (*dtos.KeyOptionsResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	resp, err := KeyOptions(ctrl.AuthApp.TwoFactor.BeginSudoKey(auth))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return resp, nil
}

// PasskeyOptions starts a passwordless sign-in.
func (ctrl Controller) PasskeyOptions(ctx *fiber.Ctx, _ struct{}) (*dtos.KeyOptionsResponse, *fiberoapi.ErrorResponse) {
	resp, err := KeyOptions(ctrl.AuthApp.TwoFactor.BeginPasskeyLogin())
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return resp, nil
}

// PasskeyLogin signs in with a passkey and returns a token. Refused
// answers count as failed logins for the IP.
func (ctrl Controller) PasskeyLogin(ctx *fiber.Ctx, req dtos.PasskeyRequest) (*dtos.LoginResponse, *fiberoapi.ErrorResponse) {
	ip := ctrl.Limits.ClientIP(ctx)
	if r, blocked := ctrl.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
		return nil, ratelimit.Refused(ctx, r)
	}
	resp, err := ctrl.AuthApp.PasskeySignIn(key(req.Ceremony, req.Credential), ctx.Get("User-Agent"), ctx.IP())
	if errors.Is(err, application.ErrInvalidKey) {
		ctrl.Limits.LoginFailed(ctx.UserContext(), ip, "")
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return resp, nil
}

// SessionPasskey is PasskeyLogin for the web UI (cookies).
func (ctrl Controller) SessionPasskey(ctx *fiber.Ctx, req dtos.PasskeyRequest) (*dtos.SessionResponse, *fiberoapi.ErrorResponse) {
	resp, errResp := ctrl.PasskeyLogin(ctx, req)
	if errResp != nil {
		return nil, errResp
	}
	return ctrl.setSession(ctx, resp.Token)
}
