package raw

import (
	"bufio"
	"errors"
	"html"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"golang.org/x/mod/module"
)

// GoModule serves GOPROXY's protocol for repositories (rules in
// application/go_proxy.go):
//
//	GET /api/packages/go/<module>/@v/list
//	GET /api/packages/go/<module>/@v/<version>.info|.mod|.zip
//	GET /api/packages/go/<module>/@latest
func (c Controller) GoModule(ctx *fiber.Ctx) error {
	rest := ctx.Params("*")
	var escaped, file string
	if p, ok := strings.CutSuffix(rest, "/@latest"); ok {
		escaped, file = p, "@latest"
	} else if p, f, ok := strings.Cut(rest, "/@v/"); ok {
		escaped, file = p, f
	} else {
		return c.goNotFound(ctx, "unknown request")
	}
	path, err := module.UnescapePath(escaped)
	if err != nil {
		return c.goNotFound(ctx, "invalid module path")
	}
	m, err := c.GoProxy.SplitModule(c.GoProxy.Host(ctx.Hostname()), path)
	if err != nil {
		return c.goNotFound(ctx, err.Error())
	}
	_, st, _, err := c.openPath(ctx, m.Repo)
	if err != nil {
		if errors.Is(err, application.ErrRepositoryNotFound) || errors.Is(err, application.ErrGitNotFound) || errors.Is(err, application.ErrGitForbidden) {
			return c.goNotFound(ctx, "module not found")
		}
		return c.fail(ctx, err)
	}

	switch {
	case file == "list":
		versions, err := m.Versions(st)
		if err != nil {
			return c.fail(ctx, err)
		}
		var b strings.Builder
		for _, v := range versions {
			b.WriteString(v.Version + "\n")
		}
		ctx.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
		return ctx.SendString(b.String())
	case file == "@latest":
		v, err := m.Latest(st)
		if err != nil {
			return c.goErr(ctx, err)
		}
		return goInfo(ctx, v)
	}
	ext := file[strings.LastIndex(file, ".")+1:]
	version, err := module.UnescapeVersion(strings.TrimSuffix(file, "."+ext))
	if err != nil {
		return c.goNotFound(ctx, "invalid version")
	}
	switch ext {
	case "info":
		v, err := m.Query(st, version)
		if err != nil {
			return c.goErr(ctx, err)
		}
		return goInfo(ctx, v)
	case "mod":
		v, err := m.Version(st, version)
		if err != nil {
			return c.goErr(ctx, err)
		}
		gomod, err := m.GoMod(v.Commit)
		if err != nil {
			return c.fail(ctx, err)
		}
		ctx.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
		return ctx.Send(gomod)
	case "zip":
		v, err := m.Version(st, version)
		if err != nil {
			return c.goErr(ctx, err)
		}
		ctx.Set(fiber.HeaderContentType, "application/zip")
		commit := v.Commit
		ctx.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			if err := gitbrowse.WriteModuleZip(w, commit, module.Version{Path: m.Path, Version: v.Version}); err != nil {
				// The status is sent: cut the body short, so the go
				// command sees a broken zip rather than a wrong one.
				c.Logger.Error().Err(err).Str("component", "http.goproxy").Str("module", m.Path).Str("version", v.Version).Msg("module zip failed")
				ctx.Context().Conn().Close()
			}
		})
		return nil
	}
	return c.goNotFound(ctx, "unknown request")
}

func goInfo(ctx *fiber.Ctx, v *application.GoVersion) error {
	return ctx.JSON(fiber.Map{"Version": v.Version, "Time": v.Time.UTC().Format(time.RFC3339)})
}

// goNotFound: the go command treats 404 and 410 as "not here" and shows
// the body.
func (c Controller) goNotFound(ctx *fiber.Ctx, msg string) error {
	return ctx.Status(fiber.StatusNotFound).SendString(msg)
}

func (c Controller) goErr(ctx *fiber.Ctx, err error) error {
	if errors.Is(err, application.ErrGoVersionNotFound) || errors.Is(err, application.ErrGoModuleNotFound) || errors.Is(err, gitbrowse.ErrEmpty) {
		return c.goNotFound(ctx, err.Error())
	}
	return c.fail(ctx, err)
}

// goGet answers `go get`'s ?go-get=1 discovery on /<namespace>/<slug>[/...]
// with a go-import "mod" tag pointing at the proxy: GOPRIVATE=<host> then
// fetches through it. It answers whether or not the repository exists,
// so it discloses nothing.
func (c Controller) goGet(ctx *fiber.Ctx) error {
	if ctx.Method() != fiber.MethodGet || ctx.Query("go-get") != "1" {
		return ctx.Next()
	}
	parts := strings.Split(strings.Trim(ctx.Path(), "/"), "/")
	if len(parts) < 2 || domain.ValidNamespaceName(parts[0]) != nil || parts[1] == "" {
		return ctx.Next()
	}
	root := c.GoProxy.Host(ctx.Hostname()) + "/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")
	proxy := ctx.BaseURL() + "/api/packages/go"
	if u := strings.TrimSuffix(c.GoProxy.Config.Server.PublicURL, "/"); u != "" {
		proxy = u + "/api/packages/go"
	}
	ctx.Set(fiber.HeaderContentType, "text/html; charset=utf-8")
	return ctx.SendString(`<!doctype html><html><head><meta name="go-import" content="` + html.EscapeString(root+" mod "+proxy) + `"></head><body>go get ` + html.EscapeString(root) + `</body></html>`)
}
