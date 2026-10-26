// Package raw serves files and archives of repositories outside the API:
//
//	GET /<owner>/<repo>/raw/<ref>/<path>
//	GET /<owner>/<repo>/archive/<ref>.zip (or .tar.gz)
//	GET /<owner>/<repo>/releases/download/<tag>/<name> (release assets)
//
// Access is git's: HTTP Basic with an access token (for scripts), the web
// UI's session cookie, or nothing for public repositories. Files are served
// so that a browser never runs them as part of this site: text as
// text/plain, unknown types as downloads, nosniff, and a CSP sandbox.
package raw

import (
	"bufio"
	"encoding/base64"
	"errors"
	"github.com/labbs/ogit/infrastructure/ratelimit"
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
	"github.com/labbs/ogit/interfaces/http/v1/releases"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	AuthApp    *application.AuthApp
	SessionApp *application.SessionApp
	BrowseApp  *application.BrowseApp
	Releases   *application.ReleaseApp
	Limits     *ratelimit.Guard
	Actions    *application.ActionsApp // workflow badges
	GoProxy    *application.GoProxyApp
}

func Setup(c Controller, app *fiber.App) {
	app.Get("/:owner/:repo/raw/*", c.Raw)
	app.Get("/:owner/:repo/archive/*", c.Archive)
	app.Get("/:owner/:repo/releases/download/*", c.ReleaseAsset)
	app.Get("/:owner/:repo/actions/workflows/:file/badge.svg", c.Badge)
	// GitHub's archive downloads (actions/checkout uses them when the job's
	// image has no git).
	app.Get("/api/v3/repos/:owner/:repo/tarball/*", c.apiArchive(gitbrowse.TarGz))
	app.Get("/api/v3/repos/:owner/:repo/zipball/*", c.apiArchive(gitbrowse.Zip))
	// The Go module proxy, and go-get meta tags for GOPRIVATE.
	app.Get("/api/packages/go/*", c.GoModule)
	app.Use(c.goGet)
}

// apiArchive serves GitHub's /tarball/<ref> and /zipball/<ref>. Besides
// git's credentials it takes "Authorization: token|Bearer <job token>",
// as GitHub's API clients send GITHUB_TOKEN.
func (c Controller) apiArchive(format string) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		auth := ctx.Get(fiber.HeaderAuthorization)
		for _, scheme := range []string{"token ", "Bearer ", "bearer "} {
			if token, ok := strings.CutPrefix(auth, scheme); ok && application.IsJobToken(token) {
				ctx.Request().Header.Set(fiber.HeaderAuthorization, "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)))
			}
		}
		ref := strings.TrimPrefix(strings.TrimPrefix(ctx.Params("*"), "refs/heads/"), "refs/tags/")
		return c.archive(ctx, ref, format)
	}
}

// errLimited: the caller used up their git budget (see ratelimit).
type errLimited struct{ r ratelimit.Result }

func (e errLimited) Error() string { return "too many requests" }

// open authorizes the caller like git does and returns the storer. Raw
// files and archives count as git operations.
func (c Controller) open(ctx *fiber.Ctx) (*domain.Repository, storer.Storer, error) {
	repo, st, _, err := c.openAs(ctx)
	return repo, st, err
}

// openAs is open with the reader ("" for anonymous).
func (c Controller) openAs(ctx *fiber.Ctx) (*domain.Repository, storer.Storer, string, error) {
	return c.openPath(ctx, ctx.Params("owner")+"/"+strings.TrimSuffix(ctx.Params("repo"), ".git"))
}

// openPath is openAs for a repository path (<namespace>/<slug>).
func (c Controller) openPath(ctx *fiber.Ctx, repoPath string) (*domain.Repository, storer.Storer, string, error) {
	ip := c.Limits.ClientIP(ctx)
	if creds := git.BasicCredentials(ctx.Get(fiber.HeaderAuthorization)); creds != nil {
		if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			return nil, nil, "", errLimited{r}
		}
		repo, pusher, err := c.AuthApp.AuthorizeGitAs(repoPath, "git-upload-pack", creds)
		if errors.Is(err, application.ErrGitUnauthenticated) {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		if err != nil {
			return nil, nil, "", err
		}
		if r := c.Limits.Allow(ctx.UserContext(), c.Limits.Rules.GitUser, "git:token:"+application.HashToken(creds.Token)[:16]); !r.Allowed {
			return nil, nil, "", errLimited{r}
		}
		st, err := c.BrowseApp.Storage.GetStorer(repo.StorageKey)
		return repo, st, pusher.UserId, err
	}
	userId := ""
	if token := ctx.Cookies(websession.SessionCookie); token != "" {
		if auth, err := c.SessionApp.ValidateToken(token); err == nil {
			userId = auth.UserID
		}
	}
	rule, key := c.Limits.Rules.GitAnonymous, "git:ip:"+ip
	if userId != "" {
		rule, key = c.Limits.Rules.GitUser, "git:user:"+userId
	}
	if r := c.Limits.Allow(ctx.UserContext(), rule, key); !r.Allowed {
		return nil, nil, "", errLimited{r}
	}
	repo, st, err := c.BrowseApp.Open(nil, &repoPath, userId)
	return repo, st, userId, err
}

func (c Controller) fail(ctx *fiber.Ctx, err error) error {
	var limited errLimited
	switch {
	case errors.As(err, &limited):
		ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(limited.r))
		return ctx.Status(fiber.StatusTooManyRequests).SendString("too many requests; try again later")
	case errors.Is(err, application.ErrGitUnauthenticated):
		ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit"`)
		return ctx.Status(fiber.StatusUnauthorized).SendString(err.Error())
	case errors.Is(err, application.ErrRepositoryNotFound), errors.Is(err, application.ErrGitNotFound),
		errors.Is(err, application.ErrGitForbidden), errors.Is(err, gitbrowse.ErrRefNotFound),
		errors.Is(err, gitbrowse.ErrPathNotFound), errors.Is(err, gitbrowse.ErrNotAFile), errors.Is(err, gitbrowse.ErrEmpty),
		errors.Is(err, application.ErrAssetNotFound):
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
	repo, st, reader, err := c.openAs(ctx)
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
	var r io.ReadCloser
	size := f.Size
	// Git LFS pointers: their content, when it is there.
	if p, ok := gitbrowse.FilePointer(f); ok {
		if lfs := c.BrowseApp.LFSFor(repo, reader); lfs != nil {
			if r, err = lfs.Open(p.Oid, p.Size); err == nil {
				size = p.Size
			} else {
				r = nil
			}
		}
	}
	if r == nil {
		if r, err = f.Reader(); err != nil {
			return c.fail(ctx, err)
		}
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
	}{br, r}, int(size))
}

func (c Controller) Archive(ctx *fiber.Ctx) error {
	repo, st, reader, err := c.openAs(ctx)
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
	return c.writeArchive(ctx, repo, st, reader, spec, format)
}

// archive serves ref of the repository in the URL as format.
func (c Controller) archive(ctx *fiber.Ctx, spec, format string) error {
	repo, st, reader, err := c.openAs(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	return c.writeArchive(ctx, repo, st, reader, spec, format)
}

func (c Controller) writeArchive(ctx *fiber.Ctx, repo *domain.Repository, st storer.Storer, reader, spec, format string) error {
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
	lfs := c.BrowseApp.LFSFor(repo, reader)
	ctx.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		if err := gitbrowse.WriteArchive(w, commit, format, prefix, lfs); err != nil {
			logger.Warn().Err(err).Str("component", "http.raw").Str("repository", repo.FullPath()).Msg("archive interrupted")
		}
		_ = w.Flush()
	})
	return nil
}

// ReleaseAsset downloads an asset of a published release (drafts' go
// through the API, for writers).
func (c Controller) ReleaseAsset(ctx *fiber.Ctx) error {
	repo, _, err := c.open(ctx)
	if err != nil {
		return c.fail(ctx, err)
	}
	tag, name, ok := releases.SplitDownload(ctx.Params("*"))
	if !ok {
		return c.fail(ctx, application.ErrAssetNotFound)
	}
	a, err := c.Releases.PublishedAsset(repo, tag, name)
	if err != nil {
		return c.fail(ctx, err)
	}
	if err := releases.SendAsset(ctx, c.Releases, a); err != nil {
		return c.fail(ctx, err)
	}
	return nil
}
