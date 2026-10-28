// Package registry serves the OCI distribution API (docker push and pull)
// under /v2 on the site's host (rules in application/packages_container.go).
package registry

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config   config.Config
	Logger   zerolog.Logger
	Packages *application.PackagesApp
	Limits   *ratelimit.Guard
}

func Setup(c Controller, app *fiber.App) {
	app.All("/v2", c.Handle)
	app.All("/v2/*", c.Handle)
}

func (c Controller) fail(ctx *fiber.Ctx, status int, code, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"errors": []fiber.Map{{"code": code, "message": msg}}})
}

func (c Controller) base(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u
	}
	return ctx.BaseURL()
}

// challenge asks the client to get a token (docker login, then retries).
func (c Controller) challenge(ctx *fiber.Ctx, scope string) error {
	u, _ := url.Parse(c.base(ctx))
	v := `Bearer realm="` + c.base(ctx) + `/v2/token",service="` + u.Host + `"`
	if scope != "" {
		v += `,scope="` + scope + `"`
	}
	ctx.Set(fiber.HeaderWWWAuthenticate, v)
	return c.fail(ctx, fiber.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

// caller reads the registry token, a token given with Basic auth, or
// nobody.
func (c Controller) caller(ctx *fiber.Ctx) (application.PackageCaller, bool, error) {
	auth := ctx.Get(fiber.HeaderAuthorization)
	if token, ok := strings.CutPrefix(auth, "Bearer "); ok {
		caller, err := c.Packages.RegistryCaller(token)
		return caller, true, err
	}
	if _, password, ok := basic(auth); ok {
		ip := c.Limits.ClientIP(ctx)
		if _, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			return application.PackageCaller{}, true, application.ErrRegistryAuth
		}
		caller, err := c.Packages.BasicCaller(password)
		if err != nil {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
			return caller, true, application.ErrRegistryAuth
		}
		return caller, true, nil
	}
	return application.PackageCaller{}, false, nil
}

func basic(header string) (string, string, bool) {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return "", "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	return user, pass, ok && pass != ""
}

var (
	manifestPath = regexp.MustCompile(`^(.+)/manifests/([^/]+)$`)
	blobPath     = regexp.MustCompile(`^(.+)/blobs/(sha256:[a-f0-9]{64})$`)
	uploadsPath  = regexp.MustCompile(`^(.+)/blobs/uploads/?$`)
	uploadPath   = regexp.MustCompile(`^(.+)/blobs/uploads/([0-9a-f-]{36})$`)
	tagsPath     = regexp.MustCompile(`^(.+)/tags/list$`)
)

// Handle routes /v2 requests.
func (c Controller) Handle(ctx *fiber.Ctx) error {
	ctx.Set("Docker-Distribution-API-Version", "registry/2.0")
	path := strings.TrimPrefix(strings.TrimPrefix(ctx.Path(), "/v2"), "/")
	if path == "token" && ctx.Method() == fiber.MethodGet {
		return c.token(ctx)
	}
	caller, _, err := c.caller(ctx)
	if err != nil {
		return c.challenge(ctx, "")
	}
	// An anonymous token reads public images; anything else asks for a
	// login.
	authenticated := caller.UserId != ""
	if path == "" {
		if !authenticated {
			return c.challenge(ctx, "")
		}
		return ctx.JSON(fiber.Map{})
	}
	m := ctx.Method()
	switch {
	case manifestPath.MatchString(path):
		p := manifestPath.FindStringSubmatch(path)
		return c.manifest(ctx, caller, authenticated, p[1], p[2])
	case uploadsPath.MatchString(path) && m == fiber.MethodPost:
		return c.startUpload(ctx, caller, authenticated, uploadsPath.FindStringSubmatch(path)[1])
	case uploadPath.MatchString(path):
		p := uploadPath.FindStringSubmatch(path)
		return c.upload(ctx, caller, authenticated, p[1], p[2])
	case blobPath.MatchString(path):
		p := blobPath.FindStringSubmatch(path)
		return c.blob(ctx, caller, authenticated, p[1], p[2])
	case tagsPath.MatchString(path) && m == fiber.MethodGet:
		return c.tags(ctx, caller, authenticated, tagsPath.FindStringSubmatch(path)[1])
	}
	return c.fail(ctx, fiber.StatusNotFound, "UNSUPPORTED", "unsupported endpoint")
}

// token is docker login's token endpoint: Basic credentials (a token as
// the password) or none.
func (c Controller) token(ctx *fiber.Ctx) error {
	_, password, ok := basic(ctx.Get(fiber.HeaderAuthorization))
	if !ok {
		password = ""
	}
	ip := c.Limits.ClientIP(ctx)
	if password != "" {
		if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
			return c.fail(ctx, fiber.StatusTooManyRequests, "TOOMANYREQUESTS", "too many failed attempts")
		}
	}
	token, ttl, err := c.Packages.RegistryToken(password)
	if err != nil {
		c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		if errors.Is(err, application.ErrTwoFactorRequired) {
			return c.fail(ctx, fiber.StatusForbidden, "DENIED", err.Error())
		}
		return c.fail(ctx, fiber.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials: use a personal access token as the password")
	}
	return ctx.JSON(fiber.Map{"token": token, "access_token": token, "expires_in": int(ttl.Seconds()), "issued_at": time.Now().UTC().Format(time.RFC3339)})
}

// image finds the image for a read or a write; anonymous callers are
// challenged rather than told it doesn't exist.
func (c Controller) image(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name string, write bool) (*domain.Package, error) {
	p, err := c.Packages.Image(name, caller, write)
	if err == nil {
		return p, nil
	}
	action := "pull"
	if write {
		action = "pull,push"
	}
	switch {
	case errors.Is(err, application.ErrNameInvalid):
		return nil, c.fail(ctx, fiber.StatusBadRequest, "NAME_INVALID", err.Error())
	case !authenticated && (errors.Is(err, application.ErrPackageNotFound) || errors.Is(err, application.ErrPackageScope)):
		return nil, c.challenge(ctx, "repository:"+name+":"+action)
	case errors.Is(err, application.ErrPackageNotFound):
		return nil, c.fail(ctx, fiber.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
	case errors.Is(err, application.ErrForbidden), errors.Is(err, application.ErrPackageScope):
		return nil, c.fail(ctx, fiber.StatusForbidden, "DENIED", "requested access to the resource is denied")
	}
	return nil, c.internal(ctx, err)
}

func (c Controller) internal(ctx *fiber.Ctx, err error) error {
	c.Logger.Error().Err(err).Str("component", "http.registry").Str("path", ctx.Path()).Msg("registry request failed")
	return c.fail(ctx, fiber.StatusInternalServerError, "UNKNOWN", "internal error")
}

func (c Controller) appErr(ctx *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, application.ErrBlobUnknown):
		return c.fail(ctx, fiber.StatusNotFound, "BLOB_UNKNOWN", err.Error())
	case errors.Is(err, application.ErrManifestUnknown):
		return c.fail(ctx, fiber.StatusNotFound, "MANIFEST_UNKNOWN", err.Error())
	case errors.Is(err, application.ErrManifestInvalid):
		return c.fail(ctx, fiber.StatusBadRequest, "MANIFEST_INVALID", err.Error())
	case errors.Is(err, application.ErrDigestInvalid):
		return c.fail(ctx, fiber.StatusBadRequest, "DIGEST_INVALID", err.Error())
	case errors.Is(err, application.ErrUploadUnknown):
		return c.fail(ctx, fiber.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", err.Error())
	case errors.Is(err, application.ErrUploadRange):
		return c.fail(ctx, fiber.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", err.Error())
	case errors.Is(err, application.ErrPackageTooLarge):
		return c.fail(ctx, fiber.StatusRequestEntityTooLarge, "SIZE_INVALID", err.Error())
	case errors.Is(err, application.ErrForbidden):
		return c.fail(ctx, fiber.StatusForbidden, "DENIED", "requested access to the resource is denied")
	}
	return c.internal(ctx, err)
}

func body(ctx *fiber.Ctx) io.Reader {
	if ctx.Request().IsBodyStream() {
		return ctx.Context().RequestBodyStream()
	}
	return bytes.NewReader(ctx.Body())
}

func (c Controller) manifest(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name, ref string) error {
	switch ctx.Method() {
	case fiber.MethodGet, fiber.MethodHead:
		p, err := c.image(ctx, caller, authenticated, name, false)
		if p == nil {
			return err
		}
		digest, mediaType, data, err := c.Packages.Manifest(p, ref)
		if err != nil {
			return c.appErr(ctx, err)
		}
		ctx.Set(fiber.HeaderContentType, mediaType)
		ctx.Set("Docker-Content-Digest", digest)
		ctx.Set(fiber.HeaderETag, `"`+digest+`"`)
		if ctx.Method() == fiber.MethodHead {
			ctx.Set(fiber.HeaderContentLength, strconv.Itoa(len(data)))
			return nil
		}
		return ctx.Send(data)
	case fiber.MethodPut:
		p, err := c.image(ctx, caller, authenticated, name, true)
		if p == nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(body(ctx), 4<<20+1))
		if err != nil {
			return c.internal(ctx, err)
		}
		digest, err := c.Packages.PutManifest(p, ref, ctx.Get(fiber.HeaderContentType), data, caller)
		if err != nil {
			return c.appErr(ctx, err)
		}
		ctx.Set(fiber.HeaderLocation, "/v2/"+name+"/manifests/"+digest)
		ctx.Set("Docker-Content-Digest", digest)
		return ctx.SendStatus(fiber.StatusCreated)
	case fiber.MethodDelete:
		p, err := c.image(ctx, caller, authenticated, name, false)
		if p == nil {
			return err
		}
		if err := c.Packages.DeleteManifest(p, ref, caller); err != nil {
			return c.appErr(ctx, err)
		}
		return ctx.SendStatus(fiber.StatusAccepted)
	}
	return c.fail(ctx, fiber.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
}

func (c Controller) blob(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name, digest string) error {
	if ctx.Method() != fiber.MethodGet && ctx.Method() != fiber.MethodHead {
		return c.fail(ctx, fiber.StatusMethodNotAllowed, "UNSUPPORTED", "blobs are deleted with their manifests")
	}
	p, err := c.image(ctx, caller, authenticated, name, false)
	if p == nil {
		if write, _ := c.Packages.Image(name, caller, true); write != nil {
			p = write // a writer's first push: the image doesn't exist yet
		} else {
			return err
		}
	}
	b, err := c.Packages.Blob(p, digest, caller)
	if err != nil {
		return c.appErr(ctx, err)
	}
	ctx.Set("Docker-Content-Digest", b.Digest)
	ctx.Set(fiber.HeaderContentType, "application/octet-stream")
	if ctx.Method() == fiber.MethodHead {
		ctx.Set(fiber.HeaderContentLength, strconv.FormatInt(b.Size, 10))
		return nil
	}
	r, err := c.Packages.OpenBlob(b)
	if err != nil {
		return c.internal(ctx, err)
	}
	return ctx.SendStream(r, int(b.Size))
}

func uploadURL(name, id string) string { return "/v2/" + name + "/blobs/uploads/" + id }

func setUploadHeaders(ctx *fiber.Ctx, name string, u *domain.ContainerUpload) {
	ctx.Set(fiber.HeaderLocation, uploadURL(name, u.Id))
	ctx.Set("Docker-Upload-UUID", u.Id)
	end := u.Size - 1
	if end < 0 {
		end = 0
	}
	ctx.Set(fiber.HeaderRange, "0-"+strconv.FormatInt(end, 10))
}

func (c Controller) startUpload(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name string) error {
	p, err := c.image(ctx, caller, authenticated, name, true)
	if p == nil {
		return err
	}
	digest := ctx.Query("digest")
	if mount := ctx.Query("mount"); mount != "" && c.Packages.Mount(p, mount, ctx.Query("from"), caller) {
		ctx.Set(fiber.HeaderLocation, "/v2/"+name+"/blobs/"+mount)
		ctx.Set("Docker-Content-Digest", mount)
		return ctx.SendStatus(fiber.StatusCreated)
	}
	u, err := c.Packages.StartUpload(p)
	if err != nil {
		return c.internal(ctx, err)
	}
	if digest != "" { // monolithic
		if u, err = c.Packages.AppendUpload(u, 0, body(ctx)); err != nil {
			return c.appErr(ctx, err)
		}
		b, err := c.Packages.FinishUpload(u, digest)
		if err != nil {
			return c.appErr(ctx, err)
		}
		ctx.Set(fiber.HeaderLocation, "/v2/"+name+"/blobs/"+b.Digest)
		ctx.Set("Docker-Content-Digest", b.Digest)
		return ctx.SendStatus(fiber.StatusCreated)
	}
	setUploadHeaders(ctx, name, u)
	return ctx.SendStatus(fiber.StatusAccepted)
}

var contentRange = regexp.MustCompile(`^(?:bytes )?(\d+)-(\d+)`)

func (c Controller) upload(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name, id string) error {
	p, err := c.image(ctx, caller, authenticated, name, true)
	if p == nil {
		return err
	}
	u, err := c.Packages.Upload(p, id)
	if err != nil {
		return c.appErr(ctx, err)
	}
	switch ctx.Method() {
	case fiber.MethodGet:
		setUploadHeaders(ctx, name, u)
		return ctx.SendStatus(fiber.StatusNoContent)
	case fiber.MethodDelete:
		c.Packages.CancelUpload(u)
		return ctx.SendStatus(fiber.StatusNoContent)
	case fiber.MethodPatch:
		offset := int64(-1)
		if m := contentRange.FindStringSubmatch(ctx.Get(fiber.HeaderContentRange)); m != nil {
			offset, _ = strconv.ParseInt(m[1], 10, 64)
		}
		if u, err = c.Packages.AppendUpload(u, offset, body(ctx)); err != nil {
			return c.appErr(ctx, err)
		}
		setUploadHeaders(ctx, name, u)
		return ctx.SendStatus(fiber.StatusAccepted)
	case fiber.MethodPut:
		if len(ctx.Body()) > 0 || ctx.Request().IsBodyStream() {
			if u, err = c.Packages.AppendUpload(u, -1, body(ctx)); err != nil {
				return c.appErr(ctx, err)
			}
		}
		b, err := c.Packages.FinishUpload(u, ctx.Query("digest"))
		if err != nil {
			return c.appErr(ctx, err)
		}
		ctx.Set(fiber.HeaderLocation, "/v2/"+name+"/blobs/"+b.Digest)
		ctx.Set("Docker-Content-Digest", b.Digest)
		return ctx.SendStatus(fiber.StatusCreated)
	}
	return c.fail(ctx, fiber.StatusMethodNotAllowed, "UNSUPPORTED", "method not allowed")
}

func (c Controller) tags(ctx *fiber.Ctx, caller application.PackageCaller, authenticated bool, name string) error {
	p, err := c.image(ctx, caller, authenticated, name, false)
	if p == nil {
		return err
	}
	tags, err := c.Packages.Tags(p)
	if err != nil {
		return c.internal(ctx, err)
	}
	// Pagination: n tags after last.
	if last := ctx.Query("last"); last != "" {
		for i, t := range tags {
			if t == last {
				tags = tags[i+1:]
				break
			}
		}
	}
	if n, err := strconv.Atoi(ctx.Query("n")); err == nil && n >= 0 && n < len(tags) {
		tags = tags[:n]
		ctx.Set(fiber.HeaderLink, `<`+"/v2/"+name+"/tags/list?n="+strconv.Itoa(n)+"&last="+url.QueryEscape(tags[len(tags)-1])+`>; rel="next"`)
	}
	return ctx.JSON(fiber.Map{"name": name, "tags": tags})
}
