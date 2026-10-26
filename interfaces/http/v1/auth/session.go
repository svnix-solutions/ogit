package auth

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
	"github.com/labbs/ogit/interfaces/http/websession"
)

// CreateSession is Login for the web UI: the token goes into cookies.
func (ctrl Controller) CreateSession(ctx *fiber.Ctx, req dtos.LoginRequest) (*dtos.SessionResponse, *fiberoapi.ErrorResponse) {
	resp, errResp := ctrl.Login(ctx, req)
	if errResp != nil {
		return nil, errResp
	}
	if resp.TwoFactorRequired {
		// No cookie yet: the code goes to /auth/session/2fa.
		return &dtos.SessionResponse{TwoFactorRequired: true, Challenge: resp.Challenge, Methods: resp.Methods}, nil
	}
	return ctrl.setSession(ctx, resp.Token)
}

// DeleteSession logs out and deletes the cookies.
func (ctrl Controller) DeleteSession(ctx *fiber.Ctx, input struct{}) (*dtos.LogoutResponse, *fiberoapi.ErrorResponse) {
	resp, errResp := ctrl.Logout(ctx, input)
	websession.Clear(ctx, websession.Secure(ctx, ctrl.Config.Server.PublicURL))
	return resp, errResp
}
