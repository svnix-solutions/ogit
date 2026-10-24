package http

import (
	"errors"
	"io/fs"
	"mime"
	"path"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// The web UI is a single-page app: files from its build are served as is,
// and every other path that no route matched gets index.html, so the app's
// router handles it. It must be registered after every other route.

const notBuilt = `<!doctype html><meta charset="utf-8"><title>oGit</title>
<p>The web UI is not built into this binary. Run <code>npm --prefix web ci && npm --prefix web run build</code>, then rebuild the server. The API is at <a href="/documentation">/documentation</a>.</p>`

func setupWebUI(app *fiber.App, files fs.FS) {
	app.Use(func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet && c.Method() != fiber.MethodHead {
			return c.Next()
		}
		p := c.Path()
		if p == "/api" || strings.HasPrefix(p, "/api/") {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"code": fiber.StatusNotFound, "details": "no such endpoint", "type": "NOT_FOUND"})
		}
		securityHeaders(c)

		name := strings.TrimPrefix(path.Clean(p), "/")
		if name != "" && name != "index.html" {
			if data, err := fs.ReadFile(files, name); err == nil {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in every asset name.
					c.Set(fiber.HeaderCacheControl, "public, max-age=31536000, immutable")
				}
				if t := mime.TypeByExtension(path.Ext(name)); t != "" {
					c.Set(fiber.HeaderContentType, t)
				}
				return c.Send(data)
			} else if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, fs.ErrInvalid) {
				return err
			}
			if strings.HasPrefix(name, "assets/") {
				return c.SendStatus(fiber.StatusNotFound) // a stale link to an old build
			}
		}

		c.Set(fiber.HeaderCacheControl, "no-cache")
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		index, err := fs.ReadFile(files, "index.html")
		if err != nil {
			return c.SendString(notBuilt)
		}
		return c.Send(index)
	})
}

func securityHeaders(c *fiber.Ctx) {
	c.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set("Referrer-Policy", "same-origin")
	c.Set("X-Frame-Options", "DENY")
}
