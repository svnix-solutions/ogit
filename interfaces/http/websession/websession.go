// Package websession lets the web UI authenticate with cookies instead of a
// bearer token that page scripts could read.
//
//   - POST /api/v1/auth/session logs in like /auth/login, but puts the token
//     in an HttpOnly cookie (ogit_session) and never returns it. It also sets
//     ogit_csrf, a random value page scripts can read.
//   - For /api/v1 requests without an Authorization header, Middleware turns
//     the cookie into one, so every handler and fiber-oapi's checks work
//     unchanged. Bearer tokens keep working and are never CSRF-checked:
//     browsers don't send them on their own.
//   - Cookie-authenticated writes must echo ogit_csrf in X-CSRF-Token (double
//     submit). Another site can make the browser send the cookies, but it
//     can't read ogit_csrf, so it can't forge the header.
package websession

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

const (
	SessionCookie = "ogit_session"
	CSRFCookie    = "ogit_csrf"
	CSRFHeader    = "X-CSRF-Token"
)

// public lists the unauthenticated endpoints: they ignore the cookie, so a
// stale one never blocks logging in again. Only POST: DELETE
// /api/v1/auth/session (logout) needs the session.
var public = map[string]bool{
	"/api/v1/auth/session":            true,
	"/api/v1/auth/login":              true,
	"/api/v1/auth/login/2fa":          true,
	"/api/v1/auth/session/2fa":        true,
	"/api/v1/auth/login/2fa/webauthn": true,
	"/api/v1/auth/passkey":            true,
	"/api/v1/auth/passkey/finish":     true,
	"/api/v1/auth/session/passkey":    true,
	"/api/v1/auth/register":           true,
	"/api/v1/auth/verify":             true,
	"/api/v1/auth/verify/resend":      true,
}

func isPublic(c *fiber.Ctx) bool {
	return c.Method() == fiber.MethodPost && public[strings.TrimSuffix(c.Path(), "/")]
}

// Middleware must be registered on /api/v1 before the routes.
func Middleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Get(fiber.HeaderAuthorization) != "" || isPublic(c) {
			return c.Next()
		}
		token := c.Cookies(SessionCookie)
		if token == "" {
			return c.Next()
		}
		if !safeMethod(c.Method()) {
			sent, want := c.Get(CSRFHeader), c.Cookies(CSRFCookie)
			if want == "" || subtle.ConstantTimeCompare([]byte(sent), []byte(want)) != 1 {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
					"code":    fiber.StatusForbidden,
					"details": "missing or invalid CSRF token: send the " + CSRFCookie + " cookie's value in the " + CSRFHeader + " header",
					"type":    "CSRF_FAILED",
				})
			}
		}
		c.Request().Header.Set(fiber.HeaderAuthorization, "Bearer "+token)
		return c.Next()
	}
}

func safeMethod(m string) bool {
	return m == fiber.MethodGet || m == fiber.MethodHead || m == fiber.MethodOptions
}

// Set stores the session token and a new CSRF value. secure must be true
// unless the UI is served over plain HTTP (local development).
func Set(c *fiber.Ctx, token string, expires time.Time, secure bool) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	c.Cookie(&fiber.Cookie{
		Name: SessionCookie, Value: token, Path: "/", Expires: expires,
		HTTPOnly: true, Secure: secure, SameSite: fiber.CookieSameSiteLaxMode,
	})
	c.Cookie(&fiber.Cookie{
		Name: CSRFCookie, Value: hex.EncodeToString(b), Path: "/", Expires: expires,
		Secure: secure, SameSite: fiber.CookieSameSiteLaxMode,
	})
	return nil
}

// Clear deletes both cookies.
func Clear(c *fiber.Ctx, secure bool) {
	for _, name := range []string{SessionCookie, CSRFCookie} {
		c.Cookie(&fiber.Cookie{
			Name: name, Value: "", Path: "/", Expires: time.Unix(0, 0), MaxAge: -1,
			HTTPOnly: name == SessionCookie, Secure: secure, SameSite: fiber.CookieSameSiteLaxMode,
		})
	}
}

// Secure reports whether cookies need the Secure flag: the request came over
// HTTPS (Fiber also reads X-Forwarded-Proto), or the configured public URL is
// HTTPS. A spoofed header can only add the flag, to the sender's own cookie.
func Secure(c *fiber.Ctx, publicURL string) bool {
	return c.Protocol() == "https" || strings.HasPrefix(publicURL, "https://")
}
