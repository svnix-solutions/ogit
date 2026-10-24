package auth

import (
	"time"

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
	expires := time.Now().Add(time.Duration(ctrl.Config.Session.ExpirationMinutes) * time.Minute)
	if err := websession.Set(ctx, resp.Token, expires, websession.Secure(ctx, ctrl.Config.Server.PublicURL)); err != nil {
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusInternalServerError, Details: "internal error", Type: "INTERNAL_ERROR"}
	}
	return &dtos.SessionResponse{ExpiresAt: expires}, nil
}

// DeleteSession logs out and deletes the cookies.
func (ctrl Controller) DeleteSession(ctx *fiber.Ctx, input struct{}) (*dtos.LogoutResponse, *fiberoapi.ErrorResponse) {
	resp, errResp := ctrl.Logout(ctx, input)
	websession.Clear(ctx, websession.Secure(ctx, ctrl.Config.Server.PublicURL))
	return resp, errResp
}
