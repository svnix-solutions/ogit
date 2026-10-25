package http

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/tokenutil"
	"github.com/labbs/ogit/infrastructure/ratelimit"
)

// apiLimit applies ratelimit.api_user to requests with a validly signed
// session token and ratelimit.api_anonymous (per IP) to the rest. The token
// is only checked for its signature (no database query); a forged one is
// anonymous. Registered after websession, so the web UI's cookie counts as
// its user.
func apiLimit(cfg config.Config, guard *ratelimit.Guard) fiber.Handler {
	return func(c *fiber.Ctx) error {
		rule, key := guard.Rules.APIAnonymous, "api:ip:"+guard.ClientIP(c)
		if token, ok := strings.CutPrefix(c.Get(fiber.HeaderAuthorization), "Bearer "); ok {
			if claims, err := tokenutil.GetClaims(token, cfg); err == nil && claims.UserID != "" {
				rule, key = guard.Rules.APIUser, "api:user:"+claims.UserID
			}
		}
		r := guard.Allow(c.UserContext(), rule, key)
		ratelimit.SetHeaders(c, r)
		if !r.Allowed {
			return ratelimit.Deny(c, r)
		}
		return c.Next()
	}
}
