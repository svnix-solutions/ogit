package git

import (
	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config        config.Config
	Logger        zerolog.Logger
	Fiber         *fiber.App
	StorageConfig storage.Config
	AuthApp       *application.AuthApp
	Limits        *ratelimit.Guard
	Webhooks      *application.WebhookApp
	Protection    *application.ProtectionApp
	Pulls         *application.PullRequestApp
	LFS           *application.LFSApp
}

// Repositories are at /<namespace>/<repo>.git. The flat /<repo>.git URLs
// used before namespaces get a 404 that says so, rather than falling through
// to the web UI.
func SetupGitRouter(controller Controller) {
	controller.Fiber.Get("/:owner/:repo/info/refs", controller.Authorize("/info/refs", ""), controller.InfoRefs)
	controller.Fiber.Post("/:owner/:repo/git-upload-pack", controller.Authorize("/git-upload-pack", "git-upload-pack"), controller.HandleUploadPack)
	controller.Fiber.Post("/:owner/:repo/git-receive-pack", controller.Authorize("/git-receive-pack", "git-receive-pack"), controller.HandleReceivePack)

	flat := func(ctx *fiber.Ctx) error {
		return ctx.Status(fiber.StatusNotFound).SendString("repository not found: repositories are addressed as /<owner>/<name>.git")
	}
	controller.Fiber.Get("/:repo/info/refs", flat)
	controller.Fiber.Post("/:repo/git-upload-pack", flat)
	controller.Fiber.Post("/:repo/git-receive-pack", flat)
}
