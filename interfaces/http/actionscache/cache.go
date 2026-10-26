// Package actionscache serves GitHub's cache API (v1, what actions/cache
// uses on servers other than github.com) under /_apis/artifactcache:
// lookups, reservations, chunk uploads and commits with the run's
// ACTIONS_RUNTIME_TOKEN, and downloads through signed URLs (HEAD and byte
// ranges, for parallel downloads).
package actionscache

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

const Base = "/_apis/artifactcache"

type Controller struct {
	Config  config.Config
	Logger  zerolog.Logger
	Actions *application.ActionsApp
}

func Setup(c Controller, app *fiber.App) {
	app.Get(Base+"/cache", c.authorized(c.lookup))
	app.Post(Base+"/caches", c.authorized(c.reserve))
	app.Patch(Base+"/caches/:id", c.authorized(c.upload))
	app.Post(Base+"/caches/:id", c.authorized(c.commit))
	app.Get(Base+"/download", c.download)
	app.Head(Base+"/download", c.download)
}

func fail(ctx *fiber.Ctx, status int, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"message": msg})
}

func (c Controller) error(ctx *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, application.ErrRuntimeToken), errors.Is(err, application.ErrArtifactSignature):
		return fail(ctx, fiber.StatusUnauthorized, err.Error())
	case errors.Is(err, application.ErrCacheNotFound):
		return fail(ctx, fiber.StatusNotFound, err.Error())
	case errors.Is(err, application.ErrCacheExists):
		return fail(ctx, fiber.StatusConflict, err.Error())
	case errors.Is(err, application.ErrCacheReadOnly):
		return fail(ctx, fiber.StatusForbidden, err.Error())
	case errors.Is(err, application.ErrCacheTooLarge):
		return fail(ctx, fiber.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, application.ErrInvalidCacheKey), errors.Is(err, application.ErrCacheIncomplete):
		return fail(ctx, fiber.StatusBadRequest, err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.actionscache").Str("path", ctx.Path()).Msg("cache request failed")
	return fail(ctx, fiber.StatusInternalServerError, "internal error")
}

func (c Controller) authorized(h func(*fiber.Ctx, *domain.ActionRun) error) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		token, _ := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
		run, err := c.Actions.RuntimeRun(token)
		if err != nil {
			return c.error(ctx, err)
		}
		return h(ctx, run)
	}
}

func (c Controller) serverURL(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u
	}
	return ctx.BaseURL()
}

// lookup: GET cache?keys=<key>,<restore keys>&version= (204 on a miss).
func (c Controller) lookup(ctx *fiber.Ctx, run *domain.ActionRun) error {
	entry, err := c.Actions.LookupCache(run, strings.Split(ctx.Query("keys"), ","), ctx.Query("version"))
	if err != nil {
		return c.error(ctx, err)
	}
	if entry == nil {
		return ctx.SendStatus(fiber.StatusNoContent)
	}
	return ctx.JSON(fiber.Map{
		"result": "hit", "cacheKey": entry.CacheKey, "scope": entry.Scope, "cacheVersion": entry.Version,
		"creationTime":    entry.CreatedAt,
		"archiveLocation": c.serverURL(ctx) + Base + "/download?" + c.Actions.SignCacheURL(entry),
	})
}

func (c Controller) reserve(ctx *fiber.Ctx, run *domain.ActionRun) error {
	var req struct {
		Key       string `json:"key"`
		Version   string `json:"version"`
		CacheSize int64  `json:"cacheSize"`
	}
	if err := json.Unmarshal(ctx.Body(), &req); err != nil { // whatever the Content-Type
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	entry, err := c.Actions.ReserveCache(run, req.Key, req.Version, req.CacheSize)
	if err != nil {
		return c.error(ctx, err)
	}
	return ctx.Status(fiber.StatusCreated).JSON(fiber.Map{"cacheId": entry.ApiId})
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(?:\*|\d+)$`)

// upload: PATCH caches/<id> with Content-Range: bytes <start>-<end>/*.
func (c Controller) upload(ctx *fiber.Ctx, run *domain.ActionRun) error {
	id, err := strconv.ParseInt(ctx.Params("id"), 10, 64)
	if err != nil {
		return c.error(ctx, application.ErrCacheNotFound)
	}
	m := contentRange.FindStringSubmatch(ctx.Get(fiber.HeaderContentRange))
	if m == nil {
		return fail(ctx, fiber.StatusBadRequest, "Content-Range: bytes <start>-<end>/* is required")
	}
	start, _ := strconv.ParseInt(m[1], 10, 64)
	end, _ := strconv.ParseInt(m[2], 10, 64)
	if end < start {
		return fail(ctx, fiber.StatusBadRequest, "invalid Content-Range")
	}
	body := io.Reader(ctx.Context().RequestBodyStream())
	if !ctx.Request().IsBodyStream() {
		body = bytes.NewReader(ctx.Body())
	}
	if err := c.Actions.UploadCacheChunk(run, id, start, io.LimitReader(body, end-start+1)); err != nil {
		return c.error(ctx, err)
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}

func (c Controller) commit(ctx *fiber.Ctx, run *domain.ActionRun) error {
	id, err := strconv.ParseInt(ctx.Params("id"), 10, 64)
	if err != nil {
		return c.error(ctx, application.ErrCacheNotFound)
	}
	var req struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal(ctx.Body(), &req); err != nil {
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	if err := c.Actions.CommitCache(run, id, req.Size); err != nil {
		return c.error(ctx, err)
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}

var byteRange = regexp.MustCompile(`^bytes=(\d*)-(\d*)$`)

// download serves an entry through its signed URL, whole or one byte
// range.
func (c Controller) download(ctx *fiber.Ctx) error {
	entry, err := c.Actions.VerifyCacheURL(ctx.Query("entry"), ctx.Query("expires"), ctx.Query("sig"))
	if err != nil {
		return c.error(ctx, err)
	}
	ctx.Set(fiber.HeaderContentType, "application/octet-stream")
	ctx.Set(fiber.HeaderAcceptRanges, "bytes")
	start, length := int64(0), entry.Size
	if rng := ctx.Get(fiber.HeaderRange); rng != "" {
		m := byteRange.FindStringSubmatch(rng)
		if m == nil || (m[1] == "" && m[2] == "") {
			return ctx.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
		}
		end := entry.Size - 1
		switch {
		case m[1] == "": // the last n bytes
			n, _ := strconv.ParseInt(m[2], 10, 64)
			start = max(entry.Size-n, 0)
		default:
			start, _ = strconv.ParseInt(m[1], 10, 64)
			if m[2] != "" {
				e, _ := strconv.ParseInt(m[2], 10, 64)
				end = min(e, entry.Size-1)
			}
		}
		if start > end || start >= entry.Size {
			ctx.Set(fiber.HeaderContentRange, fmt.Sprintf("bytes */%d", entry.Size))
			return ctx.SendStatus(fiber.StatusRequestedRangeNotSatisfiable)
		}
		length = end - start + 1
		ctx.Status(fiber.StatusPartialContent)
		ctx.Set(fiber.HeaderContentRange, fmt.Sprintf("bytes %d-%d/%d", start, end, entry.Size))
	}
	if ctx.Method() == fiber.MethodHead {
		ctx.Set(fiber.HeaderContentLength, strconv.FormatInt(length, 10))
		return nil
	}
	r, err := c.Actions.OpenCache(entry, start, length)
	if err != nil {
		return c.error(ctx, err)
	}
	return ctx.SendStream(r, int(length))
}
