package packages

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
)

// The npm registry (rules in application/packages_npm.go), for
// `npm config set @<owner>:registry <site>/api/packages/npm/`:
//
//	GET    /@scope/name                    the package document
//	PUT    /@scope/name[/-rev/<rev>]       npm publish (with a tarball); npm deprecate,
//	                                       npm unpublish <pkg>@<version> (without)
//	DELETE /@scope/name/-rev/<rev>         npm unpublish <pkg> --force
//	GET    /@scope/name/-/<name>-<v>.tgz   a tarball
//	GET|PUT|DELETE /-/package/@scope/name/dist-tags[/<tag>]
//	GET    /-/v1/search, /-/whoami, /-/ping
//
// npm sends "@scope%2fname"; both forms are accepted.
const npmPrefix = "/api/packages/npm/"

// NpmPublishRoute matches the npm routes that take large bodies.
const NpmPublishRoute = `^/api/packages/npm/@[^/]+(/|%2[fF])[^/]+(/-rev/[^/]+)?$`

func (c Controller) npmFail(ctx *fiber.Ctx, err error) error {
	var limited *optauth.ErrTooManyFailures
	status, msg := fiber.StatusInternalServerError, "internal error"
	switch {
	case errors.As(err, &limited):
		status, msg = fiber.StatusTooManyRequests, err.Error()
	case errors.Is(err, application.ErrInvalidToken):
		status, msg = fiber.StatusUnauthorized, "invalid token"
	case errors.Is(err, application.ErrPackageNotFound):
		status, msg = fiber.StatusNotFound, "not found"
	case errors.Is(err, application.ErrNpmVersionExists), errors.Is(err, application.ErrForbidden),
		errors.Is(err, application.ErrPackageScope), errors.Is(err, application.ErrTwoFactorRequired):
		status, msg = fiber.StatusForbidden, err.Error()
	case errors.Is(err, application.ErrNpmName), errors.Is(err, application.ErrNpmInvalid), errors.Is(err, application.ErrInvalidPackage),
		errors.Is(err, application.ErrNpmIntegrity), errors.Is(err, application.ErrNpmLatestTag):
		status, msg = fiber.StatusBadRequest, err.Error()
	case errors.Is(err, application.ErrPackageTooLarge):
		status, msg = fiber.StatusRequestEntityTooLarge, err.Error()
	default:
		c.Logger.Error().Err(err).Str("component", "http.npm").Str("path", ctx.Path()).Msg("npm request failed")
	}
	return ctx.Status(status).JSON(fiber.Map{"error": msg})
}

func (c Controller) npmUnauthenticated(ctx *fiber.Ctx) error {
	ctx.Set(fiber.HeaderWWWAuthenticate, `Bearer realm="ogit npm"`)
	return ctx.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required: npm config set //<host>/api/packages/npm/:_authToken <access token>"})
}

func (c Controller) registry(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u + npmPrefix
	}
	return ctx.BaseURL() + npmPrefix
}

// Npm routes /api/packages/npm/... by hand: package names contain an
// escaped or plain slash.
func (c Controller) Npm(ctx *fiber.Ctx) error {
	raw, _, _ := strings.Cut(ctx.OriginalURL(), "?")
	rest, err := url.PathUnescape(strings.TrimPrefix(raw, strings.TrimSuffix(npmPrefix, "/")))
	if err != nil {
		return c.npmFail(ctx, application.ErrPackageNotFound)
	}
	rest = strings.TrimPrefix(rest, "/")
	// npm unpublish appends the tarball URL's path to the registry's, so
	// the prefix comes twice.
	rest = strings.TrimPrefix(rest, strings.TrimPrefix(npmPrefix, "/"))
	caller, err := c.caller(ctx)
	if err != nil {
		return c.npmFail(ctx, err)
	}
	m := ctx.Method()
	signedIn := caller.UserId != ""

	if special, ok := strings.CutPrefix(rest, "-/"); ok {
		switch {
		case special == "ping":
			return ctx.JSON(fiber.Map{})
		case special == "whoami":
			if name := c.Packages.Username(caller); name != "" {
				return ctx.JSON(fiber.Map{"username": name})
			}
			return c.npmUnauthenticated(ctx)
		case special == "v1/search" && m == fiber.MethodGet:
			return c.npmSearch(ctx, caller)
		case strings.HasPrefix(special, "user/"):
			return ctx.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "npm login isn't supported: create an access token with write:packages and run npm config set //<host>/api/packages/npm/:_authToken <token>"})
		case strings.HasPrefix(special, "package/"):
			return c.npmDistTags(ctx, caller, strings.TrimPrefix(special, "package/"))
		}
		return c.npmFail(ctx, application.ErrPackageNotFound)
	}

	parts := strings.SplitN(rest, "/", 3) // "@scope", "name", rest
	if len(parts) < 2 {
		if m == fiber.MethodPut {
			return c.npmFail(ctx, application.ErrNpmName) // publishing an unscoped package
		}
		return c.npmFail(ctx, application.ErrPackageNotFound)
	}
	full := parts[0] + "/" + parts[1]
	tail := ""
	if len(parts) == 3 {
		tail = parts[2]
	}
	switch {
	case tail == "" && (m == fiber.MethodGet || m == fiber.MethodHead):
		doc, err := c.Packages.NpmPackument(full, caller, c.registry(ctx))
		if err != nil {
			return c.npmFail(ctx, err)
		}
		return ctx.JSON(doc)
	case (tail == "" || strings.HasPrefix(tail, "-rev/")) && m == fiber.MethodPut:
		if !signedIn {
			return c.npmUnauthenticated(ctx)
		}
		doc, err := c.npmDoc(ctx)
		if err != nil {
			return c.npmFail(ctx, err)
		}
		// A publish carries its tarball; the rest (npm deprecate, npm
		// unpublish <pkg>@<version>) sends the edited document, to the
		// package or its -rev URL depending on npm's version.
		if len(doc.Attachments) == 0 {
			if err := c.Packages.NpmUpdate(full, caller, doc); err != nil {
				return c.npmFail(ctx, err)
			}
			return ctx.JSON(fiber.Map{"ok": true})
		}
		if err := c.Packages.NpmPublish(full, caller, doc); err != nil {
			return c.npmFail(ctx, err)
		}
		return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{"ok": true, "id": full})
	case strings.HasPrefix(tail, "-rev/") && m == fiber.MethodDelete:
		if !signedIn {
			return c.npmUnauthenticated(ctx)
		}
		if err := c.Packages.NpmDeletePackage(full, caller); err != nil {
			return c.npmFail(ctx, err)
		}
		return ctx.JSON(fiber.Map{"ok": true})
	case strings.HasPrefix(tail, "-/"):
		file, _, isRev := strings.Cut(strings.TrimPrefix(tail, "-/"), "/-rev/")
		if isRev && m == fiber.MethodDelete {
			// npm unpublish deletes the tarball after updating the
			// document, which already removed it.
			if !signedIn {
				return c.npmUnauthenticated(ctx)
			}
			return ctx.JSON(fiber.Map{"ok": true})
		}
		if m != fiber.MethodGet && m != fiber.MethodHead {
			break
		}
		f, r, err := c.Packages.NpmTarball(full, file, caller)
		if err != nil {
			return c.npmFail(ctx, err)
		}
		ctx.Set(fiber.HeaderContentType, "application/octet-stream")
		ctx.Set("X-Content-Type-Options", "nosniff")
		if m == fiber.MethodHead {
			r.Close()
			ctx.Set(fiber.HeaderContentLength, strconv.FormatInt(f.Size, 10))
			return nil
		}
		return ctx.SendStream(r, int(f.Size))
	}
	return ctx.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "unsupported request"})
}

// npmDoc reads a publish or update body: JSON with the tarball in base64,
// so up to 4/3 of packages.max_file_size, plus the manifest.
func (c Controller) npmDoc(ctx *fiber.Ctx) (*application.NpmPublishDoc, error) {
	limit := c.Config.Packages.MaxFileSize
	if limit <= 0 {
		limit = 2 << 30
	}
	limit = limit/3*4 + 8<<20
	lr := &io.LimitedReader{R: body(ctx), N: limit + 1}
	var doc application.NpmPublishDoc
	if err := json.NewDecoder(lr).Decode(&doc); err != nil {
		if lr.N <= 0 {
			return nil, application.ErrPackageTooLarge
		}
		return nil, application.ErrNpmInvalid
	}
	return &doc, nil
}

func (c Controller) npmDistTags(ctx *fiber.Ctx, caller application.PackageCaller, rest string) error {
	// rest: @scope/name/dist-tags[/<tag>]
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || parts[2] != "dist-tags" || len(parts) > 4 {
		return c.npmFail(ctx, application.ErrPackageNotFound)
	}
	full := parts[0] + "/" + parts[1]
	if len(parts) == 3 {
		if ctx.Method() != fiber.MethodGet {
			return ctx.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "unsupported request"})
		}
		tags, err := c.Packages.NpmDistTags(full, caller)
		if err != nil {
			return c.npmFail(ctx, err)
		}
		return ctx.JSON(tags)
	}
	if caller.UserId == "" {
		return c.npmUnauthenticated(ctx)
	}
	tag := parts[3]
	switch ctx.Method() {
	case fiber.MethodPut, fiber.MethodPost:
		var version string
		if err := json.Unmarshal(ctx.Body(), &version); err != nil {
			return c.npmFail(ctx, application.ErrNpmInvalid)
		}
		if err := c.Packages.NpmSetDistTag(full, tag, version, caller); err != nil {
			return c.npmFail(ctx, err)
		}
	case fiber.MethodDelete:
		if err := c.Packages.NpmDeleteDistTag(full, tag, caller); err != nil {
			return c.npmFail(ctx, err)
		}
	default:
		return ctx.Status(fiber.StatusMethodNotAllowed).JSON(fiber.Map{"error": "unsupported request"})
	}
	return ctx.JSON(fiber.Map{"ok": true})
}

func (c Controller) npmSearch(ctx *fiber.Ctx, caller application.PackageCaller) error {
	size, _ := strconv.Atoi(ctx.Query("size", "20"))
	from, _ := strconv.Atoi(ctx.Query("from", "0"))
	size, from = min(max(size, 1), 250), max(from, 0)
	found, total, err := c.Packages.NpmSearch(ctx.Query("text"), from, size, caller)
	if err != nil {
		return c.npmFail(ctx, err)
	}
	objects := make([]fiber.Map, 0, len(found))
	for _, r := range found {
		scope, _, _ := strings.Cut(strings.TrimPrefix(r.Name, "@"), "/")
		keywords := r.Keywords
		if keywords == nil {
			keywords = []string{}
		}
		objects = append(objects, fiber.Map{
			"package": fiber.Map{"name": r.Name, "scope": scope, "version": r.Version, "description": r.Description, "keywords": keywords,
				"date": r.Date.UTC().Format(time.RFC3339), "links": fiber.Map{}, "publisher": fiber.Map{"username": scope},
				"maintainers": []fiber.Map{{"username": scope}}},
			"score":       fiber.Map{"final": 1, "detail": fiber.Map{"quality": 1, "popularity": 1, "maintenance": 1}},
			"searchScore": 1,
		})
	}
	return ctx.JSON(fiber.Map{"objects": objects, "total": total, "time": time.Now().UTC().Format(time.RFC3339)})
}
