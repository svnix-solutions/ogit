package http

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/rs/zerolog"
)

// enrollPaths are what users who must set up two-factor authentication can
// still reach: signing in and out, their profile, and the 2FA setup.
var enrollPaths = []string{"/api/v1/auth/", "/api/v1/user/2fa", "/api/v1/user/profile", "/api/v1/instance"}

// twoFactorGate refuses the API (TWO_FACTOR_REQUIRED) to signed-in users
// who must set up 2FA (application/two_factor_required.go). It only works
// when the site requires 2FA. Access tokens are refused in
// TokenApp.Authenticate instead; this covers sessions.
func twoFactorGate(sessions *application.SessionApp, twoFactor *application.TwoFactorApp, logger zerolog.Logger) fiber.Handler {
	return func(c *fiber.Ctx) error {
		for _, p := range enrollPaths {
			if strings.HasPrefix(c.Path(), p) {
				return c.Next()
			}
		}
		token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer ")
		if !ok || strings.HasPrefix(token, "ogit_") {
			return c.Next()
		}
		required, _, err := twoFactor.SiteRequirement()
		if err != nil {
			logger.Error().Err(err).Msg("cannot read the site-wide two-factor requirement")
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"code": 500, "details": "internal error", "type": "INTERNAL_ERROR"})
		}
		if !required {
			return c.Next()
		}
		auth, err := sessions.ValidateToken(token)
		if err != nil {
			return c.Next() // the route answers for invalid sessions
		}
		must, err := twoFactor.MustEnroll(auth.UserID)
		if err != nil {
			logger.Error().Err(err).Msg("cannot check two-factor authentication")
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"code": 500, "details": "internal error", "type": "INTERNAL_ERROR"})
		}
		if must {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"code": fiber.StatusForbidden, "details": application.ErrTwoFactorRequired.Error(), "type": "TWO_FACTOR_REQUIRED"})
		}
		return c.Next()
	}
}
