package v1

import (
	"github.com/labbs/ogit/infrastructure"
	"github.com/labbs/ogit/interfaces/http/v1/admin"
	"github.com/labbs/ogit/interfaces/http/v1/auth"
	"github.com/labbs/ogit/interfaces/http/v1/group"
	"github.com/labbs/ogit/interfaces/http/v1/repository"
	"github.com/labbs/ogit/interfaces/http/v1/site"
	"github.com/labbs/ogit/interfaces/http/v1/sshkey"
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
	}
	user.SetupUserRouter(userCtrl)

	repoCtrl := repository.Controller{
		Config:        deps.Config,
		Logger:        deps.Logger,
		FiberOapi:     repoGrp,
		RepositoryApp: deps.RepoApp,
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

	site.Setup(site.Controller{Config: deps.Config, Logger: deps.Logger, SessionApp: deps.SessionApp, RepositoryApp: deps.RepoApp}, grp)
}
