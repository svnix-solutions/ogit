// Package packages serves the package formats' own routes:
// /api/packages/<owner>/generic/<name>/<version>/<file> (rules in
// application/packages.go) and the npm registry under /api/packages/npm/
// (npm.go).
package packages

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config     config.Config
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Packages   *application.PackagesApp
	Limits     *ratelimit.Guard
}

func Setup(c Controller, app *fiber.App) {
	// Generic files: /api/packages/<owner>/generic/<name>/<version>/<file>,
	// where the owner may be a sub-group's path (org/team).
	app.All("/api/packages/*", c.Generic)
	app.All("/api/packages/npm/*", c.Npm)
}

// genericRef is what a generic file route names.
type genericRef struct{ owner, name, version, file string }

// parseGeneric reads a generic route: "generic" comes 3 segments before
// the end for files, 2 for a version (DELETE).
func parseGeneric(ctx *fiber.Ctx) (genericRef, bool) {
	parts := strings.Split(strings.TrimPrefix(ctx.Path(), "/api/packages/"), "/")
	tail := 3
	if ctx.Method() == fiber.MethodDelete {
		tail = 2
	}
	k := len(parts) - tail - 1
	if k < 1 || parts[k] != "generic" {
		return genericRef{}, false
	}
	for _, p := range parts {
		if p == "" {
			return genericRef{}, false
		}
	}
	r := genericRef{owner: strings.Join(parts[:k], "/"), name: parts[k+1], version: parts[k+2]}
	if tail == 3 {
		r.file = parts[k+3]
	}
	return r, true
}

// Generic serves generic files, and passes other package routes on.
func (c Controller) Generic(ctx *fiber.Ctx) error {
	ref, ok := parseGeneric(ctx)
	if !ok {
		return ctx.Next()
	}
	switch ctx.Method() {
	case fiber.MethodPut:
		return c.PutGeneric(ctx, ref)
	case fiber.MethodGet, fiber.MethodHead:
		return c.GetGeneric(ctx, ref)
	case fiber.MethodDelete:
		return c.DeleteGeneric(ctx, ref)
	}
	return ctx.Next()
}

// caller: a token (any form optauth accepts), else for downloads the web
// UI's session cookie (no CSRF token on these routes, so never for writes).
func (c Controller) caller(ctx *fiber.Ctx) (application.PackageCaller, error) {
	if optauth.Token(ctx) != "" {
		return optauth.Caller(ctx, c.SessionApp, c.TokenApp, c.Limits)
	}
	if ctx.Method() == fiber.MethodGet || ctx.Method() == fiber.MethodHead {
		if token := ctx.Cookies(websession.SessionCookie); token != "" {
			if auth, err := c.SessionApp.ValidateToken(token); err == nil {
				return application.PackageCaller{UserId: auth.UserID}, nil
			}
		}
	}
	return application.PackageCaller{}, nil
}

func (c Controller) fail(ctx *fiber.Ctx, err error) error {
	var limited *optauth.ErrTooManyFailures
	if errors.As(err, &limited) {
		return ctx.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"code": fiber.StatusTooManyRequests, "type": "TOO_MANY_REQUESTS", "details": err.Error()})
	}
	e := apierror.From(err, c.Logger)
	if e.Code == fiber.StatusUnauthorized || e.Code == fiber.StatusNotFound {
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit packages"`)
	}
	return ctx.Status(e.Code).JSON(e)
}

func (c Controller) PutGeneric(ctx *fiber.Ctx, ref genericRef) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	if caller.UserId == "" {
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit packages"`)
		return ctx.Status(fiber.StatusUnauthorized).JSON(apierror.Unauthenticated())
	}
	p, err := c.Packages.ForWrite(ref.owner, domain.PackageGeneric, ref.name, caller)
	if err != nil {
		return c.fail(ctx, err)
	}
	f, err := c.Packages.PutFile(p, ref.version, ref.file, caller, ctx.Get(fiber.HeaderContentType), body(ctx))
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{"name": f.Name, "size": f.Size, "sha256": f.Sha256})
}

func (c Controller) GetGeneric(ctx *fiber.Ctx, ref genericRef) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	p, err := c.Packages.Readable(ref.owner, domain.PackageGeneric, ref.name, caller)
	if err != nil {
		return c.fail(ctx, err)
	}
	f, r, err := c.Packages.OpenFile(p, ref.version, ref.file)
	if err != nil {
		return c.fail(ctx, err)
	}
	// Never the uploaded type: a file must not render on this origin.
	ctx.Set(fiber.HeaderContentType, "application/octet-stream")
	ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
	ctx.Set("X-Content-Type-Options", "nosniff")
	ctx.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctx.Set(fiber.HeaderETag, `"`+f.Sha256+`"`)
	ctx.Set("X-Checksum-Sha256", f.Sha256)
	if ctx.Method() == fiber.MethodHead {
		r.Close()
		ctx.Set(fiber.HeaderContentLength, strconv.FormatInt(f.Size, 10))
		return nil
	}
	return ctx.SendStream(r, int(f.Size))
}

func (c Controller) DeleteGeneric(ctx *fiber.Ctx, ref genericRef) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	if caller.UserId == "" {
		return ctx.Status(fiber.StatusUnauthorized).JSON(apierror.Unauthenticated())
	}
	if err := c.Packages.DeleteVersion(ref.owner, domain.PackageGeneric, ref.name, ref.version, caller); err != nil {
		return c.fail(ctx, err)
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}

func body(ctx *fiber.Ctx) io.Reader {
	if ctx.Request().IsBodyStream() {
		return ctx.Context().RequestBodyStream()
	}
	return bytes.NewReader(ctx.Body())
}
