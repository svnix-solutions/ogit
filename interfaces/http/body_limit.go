package http

import (
	"bytes"
	"io"
	"regexp"

	"github.com/gofiber/fiber/v2"
)

// streamedRoutes may read request bodies larger than fiber's BodyLimit, as
// a stream: release asset uploads (they enforce releases.max_asset_size)
// and git pushes (git.max_push_size).
var streamedRoutes = regexp.MustCompile(`^(/api/(v1/repository/[^/]+|v3/repos/[^/]+/[^/]+|uploads/repos/[^/]+/[^/]+)/releases/[^/]+/assets/?|/[^/]+/[^/]+/git-receive-pack)$`)

// lfsUploadRoute: PUT uploads that are streamed: Git LFS objects
// (lfs.max_object_size) and Actions artifacts (actions.max_artifact_size).
var lfsUploadRoute = regexp.MustCompile(`^/[^/]+/[^/]+/info/lfs/objects/[0-9a-f]{64}$|^/twirp/github\.actions\.results\.api\.v1\.ArtifactService/UploadArtifact$`)

// cacheUploadRoute: actions/cache chunk uploads (32 MB each).
var cacheUploadRoute = regexp.MustCompile(`^/_apis/artifactcache/caches/[0-9]+$`)

// bodyLimit restores the BodyLimit that StreamRequestBody lifts: fasthttp
// streams bodies over the limit (and chunked ones) instead of refusing
// them, and ctx.Body() would then read them whole into memory. Other
// routes get the body read up to the limit, as before, or 413.
func bodyLimit(limit int) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		req := ctx.Request()
		if !req.IsBodyStream() {
			return ctx.Next()
		}
		if ctx.Method() == fiber.MethodPost && streamedRoutes.MatchString(ctx.Path()) ||
			ctx.Method() == fiber.MethodPut && lfsUploadRoute.MatchString(ctx.Path()) ||
			ctx.Method() == fiber.MethodPatch && cacheUploadRoute.MatchString(ctx.Path()) {
			// A handler may answer without reading the whole body (refused,
			// not found). fasthttp would then parse the rest as the next
			// request on this connection, a desync behind a proxy that
			// reuses connections: close it instead.
			err := ctx.Next()
			ctx.Context().SetConnectionClose()
			return err
		}
		var buf bytes.Buffer
		n, err := io.Copy(&buf, io.LimitReader(req.BodyStream(), int64(limit)+1))
		if err != nil {
			ctx.Context().SetConnectionClose()
			return ctx.Status(fiber.StatusBadRequest).SendString("could not read the request body")
		}
		if n > int64(limit) {
			ctx.Context().SetConnectionClose()
			return fiber.ErrRequestEntityTooLarge
		}
		req.SetBody(buf.Bytes())
		return ctx.Next()
	}
}
