// Package packages serves the package formats' own routes:
// /api/packages/<owner>/generic/<name>/<version>/<file> (rules in
// application/packages.go).
package packages

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Packages   *application.PackagesApp
	Limits     *ratelimit.Guard
}

// GenericRoute is the generic file route (its PUT is streamed).
const GenericRoute = "/api/packages/:owner/generic/:name/:version/:file"

func Setup(c Controller, app *fiber.App) {
	app.Put(GenericRoute, c.PutGeneric)
	app.Get(GenericRoute, c.GetGeneric)
	app.Head(GenericRoute, c.GetGeneric)
	app.Delete("/api/packages/:owner/generic/:name/:version", c.DeleteGeneric)
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

func (c Controller) PutGeneric(ctx *fiber.Ctx) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	if caller.UserId == "" {
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit packages"`)
		return ctx.Status(fiber.StatusUnauthorized).JSON(apierror.Unauthenticated())
	}
	p, err := c.Packages.ForWrite(ctx.Params("owner"), domain.PackageGeneric, ctx.Params("name"), caller)
	if err != nil {
		return c.fail(ctx, err)
	}
	f, err := c.Packages.PutFile(p, ctx.Params("version"), ctx.Params("file"), caller, ctx.Get(fiber.HeaderContentType), body(ctx))
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{"name": f.Name, "size": f.Size, "sha256": f.Sha256})
}

func (c Controller) GetGeneric(ctx *fiber.Ctx) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	p, err := c.Packages.Readable(ctx.Params("owner"), domain.PackageGeneric, ctx.Params("name"), caller)
	if err != nil {
		return c.fail(ctx, err)
	}
	f, r, err := c.Packages.OpenFile(p, ctx.Params("version"), ctx.Params("file"))
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

func (c Controller) DeleteGeneric(ctx *fiber.Ctx) error {
	caller, err := c.caller(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	if caller.UserId == "" {
		return ctx.Status(fiber.StatusUnauthorized).JSON(apierror.Unauthenticated())
	}
	if err := c.Packages.DeleteVersion(ctx.Params("owner"), domain.PackageGeneric, ctx.Params("name"), ctx.Params("version"), caller); err != nil {
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
