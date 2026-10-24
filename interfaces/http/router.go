package http

import (
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/interfaces/http/git"
	"github.com/labbs/ogit/interfaces/http/raw"
	v1 "github.com/labbs/ogit/interfaces/http/v1"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/labbs/ogit/web"
)

func SetupRoutes(deps infrastructure.Deps) {
	logger := deps.Logger.With().Str("component", "http.router").Logger()
	logger.Info().Str("event", "setup_routes").Msg("Setting up HTTP routes")

	// Before any /api/v1 route: turns the web UI's cookie into a bearer token.
	deps.Http.Fiber.Use("/api/v1", websession.Middleware())

	// Setup system routes (health, metrics, etc.)
	setupSystemRoutes(deps)

	// Setup git routes
	gitCtrl := git.Controller{
		Config:        deps.Config,
		Logger:        deps.Logger,
		Fiber:         deps.Http.Fiber,
		StorageConfig: deps.Storage,
		AuthApp:       deps.AuthApp,
	}
	git.SetupGitRouter(gitCtrl)

	// Setup v1 routes
	v1.SetupRouterV1(deps)

	// Raw files and archives: /<owner>/<repo>/raw/... and /archive/...
	raw.Setup(raw.Controller{Logger: deps.Logger, AuthApp: deps.AuthApp, SessionApp: deps.SessionApp, BrowseApp: deps.BrowseApp}, deps.Http.Fiber)

	// Last: it answers every GET no other route matched.
	setupWebUI(deps.Http.Fiber, web.Dist())
}
