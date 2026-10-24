// Package raw serves files and archives of repositories outside the API:
//
//	GET /<owner>/<repo>/raw/<ref>/<path>
//	GET /<owner>/<repo>/archive/<ref>.zip (or .tar.gz)
//
// Access is git's: HTTP Basic with an access token (for scripts), the web
// UI's session cookie, or nothing for public repositories. Files are served
// so that a browser never runs them as part of this site: text as
// text/plain, unknown types as downloads, nosniff, and a CSP sandbox.
package raw

import (
	"bufio"
	"errors"
	"io"
	"mime"
	"path"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/interfaces/http/git"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	AuthApp    *application.AuthApp
	SessionApp *application.SessionApp
	BrowseApp  *application.BrowseApp
}

func Setup(c Controller, app *fiber.App) {
	app.Get("/:owner/:repo/raw/*", c.Raw)
	app.Get("/:owner/:repo/archive/*", c.Archive)
}

// open authorizes the caller like git does and returns the storer.
func (c Controller) open(ctx *fiber.Ctx) (*domain.Repository, storer.Storer, error) {
	repoPath := ctx.Params("owner") + "/" + strings.TrimSuffix(ctx.Params("repo"), ".git")
	if creds := git.BasicCredentials(ctx.Get(fiber.HeaderAuthorization)); creds != nil {
		repo, err := c.AuthApp.AuthorizeGit(repoPath, "git-upload-pack", creds)
		if err != nil {
			return nil, nil, err
		}
		st, err := c.BrowseApp.Storage.GetStorer(repo.StorageKey)
		return repo, st, err
	}
	userId := ""
	if token := ctx.Cookies(websession.SessionCookie); token != "" {
		if auth, err := c.SessionApp.ValidateToken(token); err == nil {
			userId = auth.UserID
		}
	}
	return c.BrowseApp.Open(nil, &repoPath, userId)
}

func (c Controller) fail(ctx *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, application.ErrGitUnauthenticated):
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit"`)
		return ctx.Status(fiber.StatusUnauthorized).SendString(err.Error())
	case errors.Is(err, application.ErrRepositoryNotFound), errors.Is(err, application.ErrGitNotFound),
		errors.Is(err, application.ErrGitForbidden), errors.Is(err, gitbrowse.ErrRefNotFound),
		errors.Is(err, gitbrowse.ErrPathNotFound), errors.Is(err, gitbrowse.ErrNotAFile), errors.Is(err, gitbrowse.ErrEmpty):
		return ctx.Status(fiber.StatusNotFound).SendString("not found")
	case errors.Is(err, gitbrowse.ErrFormat):
		return ctx.Status(fiber.StatusNotFound).SendString(err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.raw").Str("path", ctx.Path()).Msg("raw request failed")
	return ctx.Status(fiber.StatusInternalServerError).SendString("internal error")
}

// inlineTypes are shown in the browser; everything else is downloaded.
// Scripts can't run in them: images, and text as text/plain.
var inlineTypes = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true, ".pdf": true,
}

func (c Controller) Raw(ctx *fiber.Ctx) error {
	_, st, err := c.open(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	_, filePath, commit, err := gitbrowse.SplitRefPath(st, ctx.Params("*"))
	if err != nil {
		return c.fail(ctx, err)
	}
	f, err := gitbrowse.OpenFile(commit, filePath)
	if err != nil {
		return c.fail(ctx, err)
	}
	r, err := f.Reader()
	if err != nil {
		return c.fail(ctx, err)
	}

	// Decide the type from the start of the content and the extension.
	br := bufio.NewReaderSize(r, 8000)
	head, _ := br.Peek(8000)
	ext := strings.ToLower(path.Ext(filePath))
	name := map[string]string{"filename": path.Base(filePath)}
	switch {
	case inlineTypes[ext]:
		ctx.Set(fiber.HeaderContentType, mime.TypeByExtension(ext))
		ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("inline", name))
	case !gitbrowse.IsBinary(head):
		ctx.Set(fiber.HeaderContentType, "text/plain; charset=utf-8")
	default:
		ctx.Set(fiber.HeaderContentType, "application/octet-stream")
		ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", name))
	}
	ctx.Set("X-Content-Type-Options", "nosniff")
	ctx.Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; sandbox")
	ctx.Set(fiber.HeaderCacheControl, "private, no-cache")
	// The body is read after this returns; fasthttp closes it at the end.
	return ctx.SendStream(struct {
		io.Reader
		io.Closer
	}{br, r}, int(f.Size))
}

func (c Controller) Archive(ctx *fiber.Ctx) error {
	repo, st, err := c.open(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	spec := ctx.Params("*")
	var format string
	for _, f := range []string{gitbrowse.TarGz, gitbrowse.Zip} {
		if strings.HasSuffix(spec, "."+f) {
			format, spec = f, strings.TrimSuffix(spec, "."+f)
			break
		}
	}
	if format == "" {
		return c.fail(ctx, gitbrowse.ErrFormat)
	}
	commit, err := gitbrowse.ResolveRef(st, spec)
	if err != nil {
		return c.fail(ctx, err)
	}
	prefix := repo.Slug + "-" + strings.ReplaceAll(spec, "/", "-")
	ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": prefix + "." + format}))
	if format == gitbrowse.Zip {
		ctx.Set(fiber.HeaderContentType, "application/zip")
	} else {
		ctx.Set(fiber.HeaderContentType, "application/gzip")
	}
	ctx.Set("X-Content-Type-Options", "nosniff")
	logger := c.Logger
	ctx.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		if err := gitbrowse.WriteArchive(w, commit, format, prefix); err != nil {
			logger.Warn().Err(err).Str("component", "http.raw").Str("repository", repo.FullPath()).Msg("archive interrupted")
		}
		_ = w.Flush()
	})
	return nil
}
