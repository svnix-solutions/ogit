package user

import (
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config    config.Config
	Logger    zerolog.Logger
	FiberOapi *fiberoapi.OApiGroup
	UserApp   *application.UserApp
	TokenApp  *application.TokenApp
	AuthApp   *application.AuthApp
	TwoFactor *application.TwoFactorApp
	Limits    *ratelimit.Guard
}

func SetupUserRouter(controller Controller) {
	setupTwoFactorRoutes(controller)

	fiberoapi.Get(controller.FiberOapi, "/profile", controller.GetProfile, fiberoapi.OpenAPIOptions{
		Summary:     "Get user profile",
		Description: "Retrieve the profile of the authenticated user",
		OperationID: "user.getProfile",
		Tags:        []string{"User"},
	})

	fiberoapi.Post(controller.FiberOapi, "/token", controller.CreateToken, fiberoapi.OpenAPIOptions{
		Summary:     "Create access token",
		Description: "Create a personal access token for git over HTTP (username: token name, password: token). The token is only returned once.",
		OperationID: "user.createToken",
		Tags:        []string{"User"},
	})

	fiberoapi.Get(controller.FiberOapi, "/tokens", controller.ListTokens, fiberoapi.OpenAPIOptions{
		Summary:     "List access tokens",
		Description: "Your personal access tokens. The secrets are never shown again.",
		OperationID: "user.listTokens",
		Tags:        []string{"User"},
	})

	fiberoapi.Delete(controller.FiberOapi, "/tokens/:token_id", controller.RevokeToken, fiberoapi.OpenAPIOptions{
		Summary:     "Revoke access token",
		Description: "The token stops working immediately.",
		OperationID: "user.revokeToken",
		Tags:        []string{"User"},
	})

	fiberoapi.Post(controller.FiberOapi, "/password", controller.ChangePassword, fiberoapi.OpenAPIOptions{
		Summary:     "Change password",
		Description: "Requires the current password. The password policy applies. All other sessions of the user end.",
		OperationID: "user.changePassword",
		Tags:        []string{"User"},
	})
}
