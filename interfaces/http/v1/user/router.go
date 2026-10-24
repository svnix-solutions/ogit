package user

import (
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config    config.Config
	Logger    zerolog.Logger
	FiberOapi *fiberoapi.OApiGroup
	UserApp   *application.UserApp
	TokenApp  *application.TokenApp
}

func SetupUserRouter(controller Controller) {
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
}
