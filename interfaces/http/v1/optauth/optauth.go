// Package optauth reads an optional session on routes that don't require
// one (Security: "disabled" in fiber-oapi, which has no optional auth).
package optauth

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
)

// UserID returns the caller's user id from a bearer token (or the web UI's
// cookie, which websession turned into one), or "" when there is none or it
// is invalid: the request is then anonymous.
func UserID(ctx *fiber.Ctx, sessions *application.SessionApp) string {
	token, ok := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
	if !ok || token == "" {
		return ""
	}
	auth, err := sessions.ValidateToken(token)
	if err != nil {
		return ""
	}
	return auth.UserID
}
