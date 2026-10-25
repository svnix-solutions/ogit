package http

import (
	"bytes"
	"io"
	"regexp"

	"github.com/gofiber/fiber/v2"
)

// streamedRoutes may read request bodies larger than fiber's BodyLimit, as
// a stream: release asset uploads (they enforce releases.max_asset_size).
var streamedRoutes = regexp.MustCompile(`^/api/(v1/repository/[^/]+|v3/repos/[^/]+/[^/]+|uploads/repos/[^/]+/[^/]+)/releases/[^/]+/assets/?$`)

// bodyLimit restores the BodyLimit that StreamRequestBody lifts: fasthttp
// streams bodies over the limit (and chunked ones) instead of refusing
// them, and ctx.Body() would then read them whole into memory. Other
// routes get the body read up to the limit, as before, or 413.
func bodyLimit(limit int) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		req := ctx.Request()
		if !req.IsBodyStream() || (ctx.Method() == fiber.MethodPost && streamedRoutes.MatchString(ctx.Path())) {
			return ctx.Next()
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
