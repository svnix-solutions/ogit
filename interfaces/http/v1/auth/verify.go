package auth

import (
	"errors"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"html"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
)

// VerifyEmail consumes a verification token (for API clients and a future
// web UI). The link sent by email is served by VerifyEmailPage.
func (ctrl Controller) VerifyEmail(ctx *fiber.Ctx, req dtos.VerifyEmailRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	err := ctrl.AuthApp.VerifyEmail(req.Token)
	if errors.Is(err, application.ErrInvalidVerificationToken) {
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusBadRequest, Details: err.Error(), Type: "INVALID_VERIFICATION_TOKEN"}
	} else if err != nil {
		ctrl.Logger.Error().Err(err).Str("component", "http.api.v1.auth.verify").Msg("failed to verify email")
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusInternalServerError, Details: "internal error", Type: "INTERNAL_ERROR"}
	}
	return &dtos.MessageResponse{Message: "Email verified. You can now log in."}, nil
}

// ResendVerification always answers the same, whether or not the email
// belongs to an unverified account.
func (ctrl Controller) ResendVerification(ctx *fiber.Ctx, req dtos.ResendVerificationRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	// Each resend sends an email: limited per IP (and per account inside).
	if r := ctrl.Limits.Allow(ctx.UserContext(), ctrl.Limits.Rules.RegisterIP, "register:ip:"+ctrl.Limits.ClientIP(ctx)); !r.Allowed {
		return nil, ratelimit.Refused(ctx, r)
	}
	_ = ctrl.AuthApp.ResendVerification(req.Email)
	return &dtos.MessageResponse{Message: "If an unverified account exists for this address, a new verification email has been sent."}, nil
}

// VerifyEmailPage handles GET /api/v1/auth/verify?token=..., the link in the
// verification email, and answers with a small HTML page.
func (ctrl Controller) VerifyEmailPage(ctx *fiber.Ctx) error {
	err := ctrl.AuthApp.VerifyEmail(ctx.Query("token"))
	status, title, text := fiber.StatusOK, "Email verified", "Your email address is verified. You can now log in."
	switch {
	case errors.Is(err, application.ErrInvalidVerificationToken):
		status, title, text = fiber.StatusBadRequest, "Link not valid", "This verification link is invalid, expired or already used. Request a new one and try again."
	case err != nil:
		ctrl.Logger.Error().Err(err).Str("component", "http.auth.verify_page").Msg("failed to verify email")
		status, title, text = fiber.StatusInternalServerError, "Something went wrong", "The email address could not be verified. Try again later."
	}

	ctx.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	ctx.Set("Referrer-Policy", "no-referrer") // keep the token out of Referer headers
	return ctx.Status(status).SendString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` +
		html.EscapeString(title) + ` - oGit</title></head><body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem"><h1>` +
		html.EscapeString(title) + `</h1><p>` + html.EscapeString(text) + `</p></body></html>`)
}
