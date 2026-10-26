package http

import (
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/interfaces/http/ghcompat"
	"github.com/labbs/ogit/interfaces/http/git"
	"github.com/labbs/ogit/interfaces/http/lfs"
	"github.com/labbs/ogit/interfaces/http/raw"
	v1 "github.com/labbs/ogit/interfaces/http/v1"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/labbs/ogit/web"
)

func SetupRoutes(deps infrastructure.Deps) {
	logger := deps.Logger.With().Str("component", "http.router").Logger()
	logger.Info().Str("event", "setup_routes").Msg("Setting up HTTP routes")

	// First: bodies over the usual limit only reach the upload routes.
	deps.Http.Fiber.Use(bodyLimit(deps.Http.Fiber.Config().BodyLimit))

	// Before any /api/v1 route: turns the web UI's cookie into a bearer token.
	deps.Http.Fiber.Use("/api/v1", websession.Middleware())
	// Users who must set up 2FA can only reach its setup.
	deps.Http.Fiber.Use("/api/v1", twoFactorGate(deps.SessionApp, deps.TwoFactorApp, deps.Logger))
	// Then the request budget per user (or IP).
	deps.Http.Fiber.Use("/api/v1", apiLimit(deps.Config, deps.Limits))

	// Setup system routes (health, metrics, etc.)
	setupSystemRoutes(deps)

	// Setup git routes
	gitCtrl := git.Controller{
		Config:        deps.Config,
		Logger:        deps.Logger,
		Fiber:         deps.Http.Fiber,
		StorageConfig: deps.Storage,
		AuthApp:       deps.AuthApp,
		Limits:        deps.Limits,
		Webhooks:      deps.WebhookApp,
		Protection:    deps.ProtectionApp,
		Pulls:         deps.PullApp,
		LFS:           deps.LFSApp,
	}
	git.SetupGitRouter(gitCtrl)

	// Git LFS: /<owner>/<repo>.git/info/lfs/...
	lfs.Setup(lfs.Controller{Config: deps.Config, Logger: deps.Logger, LFS: deps.LFSApp, Limits: deps.Limits}, deps.Http.Fiber)

	// Setup v1 routes
	v1.SetupRouterV1(deps)

	// GitHub-compatible commit statuses and releases for CI (tokens only).
	deps.Http.Fiber.Use("/api/v3", apiLimit(deps.Config, deps.Limits))
	deps.Http.Fiber.Use("/api/uploads", apiLimit(deps.Config, deps.Limits))
	ghcompat.Setup(ghcompat.Controller{Config: deps.Config, Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp,
		Limits: deps.Limits, Statuses: deps.StatusApp, Releases: deps.ReleaseApp}, deps.Http.Fiber)

	// Raw files and archives: /<owner>/<repo>/raw/... and /archive/...
	raw.Setup(raw.Controller{Logger: deps.Logger, AuthApp: deps.AuthApp, SessionApp: deps.SessionApp, BrowseApp: deps.BrowseApp,
		Releases: deps.ReleaseApp, Limits: deps.Limits}, deps.Http.Fiber)

	// Last: it answers every GET no other route matched.
	setupWebUI(deps.Http.Fiber, web.Dist())
}
