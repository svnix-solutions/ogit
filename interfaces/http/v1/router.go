package v1

import (
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/interfaces/http/v1/admin"
	"github.com/labbs/ogit/interfaces/http/v1/auth"
	"github.com/labbs/ogit/interfaces/http/v1/browse"
	"github.com/labbs/ogit/interfaces/http/v1/group"
	"github.com/labbs/ogit/interfaces/http/v1/issues"
	"github.com/labbs/ogit/interfaces/http/v1/milestones"
	"github.com/labbs/ogit/interfaces/http/v1/pulls"
	"github.com/labbs/ogit/interfaces/http/v1/releases"
	"github.com/labbs/ogit/interfaces/http/v1/repository"
	"github.com/labbs/ogit/interfaces/http/v1/site"
	"github.com/labbs/ogit/interfaces/http/v1/sshkey"
	"github.com/labbs/ogit/interfaces/http/v1/statuses"
	"github.com/labbs/ogit/interfaces/http/v1/user"
)

func SetupRouterV1(deps infrastructure.Deps) {
	deps.Logger.Info().Str("component", "http.router.v1").Msg("Setting up API v1 routes")
	grp := deps.Http.FiberOapi.Group("/api/v1")

	authCtrl := auth.Controller{
		Config:    deps.Config,
		Logger:    deps.Logger,
		FiberOapi: grp.Group("/auth"),
		AuthApp:   deps.AuthApp,
		Limits:    deps.Limits,
	}
	auth.SetupAuthRouter(authCtrl)
	// The link in verification emails: a plain page, not an OpenAPI operation.
	deps.Http.Fiber.Get("/api/v1/auth/verify", authCtrl.VerifyEmailPage)

	userGrp, repoGrp, adminGrp := grp.Group("/user"), grp.Group("/repository"), grp.Group("/admin")

	userCtrl := user.Controller{
		Config:    deps.Config,
		Logger:    deps.Logger,
		FiberOapi: userGrp,
		UserApp:   deps.UserApp,
		TokenApp:  deps.TokenApp,
		AuthApp:   deps.AuthApp,
		Limits:    deps.Limits,
	}
	user.SetupUserRouter(userCtrl)

	repoCtrl := repository.Controller{
		Config:        deps.Config,
		Logger:        deps.Logger,
		FiberOapi:     repoGrp,
		RepositoryApp: deps.RepoApp,
		SessionApp:    deps.SessionApp,
		TokenApp:      deps.TokenApp,
		RepoTokens:    deps.RepoTokenApp,
		WebhookApp:    deps.WebhookApp,
		ProtectionApp: deps.ProtectionApp,
	}
	repository.SetupRepositoryRouter(repoCtrl)

	group.SetupGroupRouter(group.Controller{
		Config:    deps.Config,
		Logger:    deps.Logger,
		FiberOapi: grp.Group("/group"),
		GroupApp:  deps.GroupApp,
	})

	admin.SetupAdminRouter(admin.Controller{
		Logger:    deps.Logger,
		FiberOapi: adminGrp,
		AdminApp:  deps.AdminApp,
	})

	sshkey.Setup(sshkey.Controller{Logger: deps.Logger, SSHKeyApp: deps.SSHKeyApp}, userGrp, repoGrp, adminGrp)

	browse.Setup(browse.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, BrowseApp: deps.BrowseApp}, repoGrp)

	statuses.Setup(statuses.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, Limits: deps.Limits, Statuses: deps.StatusApp}, repoGrp)

	releaseCtrl := releases.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, Limits: deps.Limits,
		Releases: deps.ReleaseApp, PublicURL: deps.Config.Server.PublicURL}
	releases.Setup(releaseCtrl, repoGrp)
	releases.SetupFiles(releaseCtrl, deps.Http.Fiber)

	milestones.Setup(milestones.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, Milestones: deps.MilestoneApp}, repoGrp)

	issues.Setup(issues.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, Issues: deps.IssueApp}, repoGrp)

	pulls.Setup(pulls.Controller{Logger: deps.Logger, SessionApp: deps.SessionApp, TokenApp: deps.TokenApp, Pulls: deps.PullApp}, repoGrp)

	site.Setup(site.Controller{Config: deps.Config, Logger: deps.Logger, SessionApp: deps.SessionApp, RepositoryApp: deps.RepoApp}, grp)
}
