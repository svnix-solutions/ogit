package auth

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
	AuthApp   *application.AuthApp
}

func SetupAuthRouter(controller Controller) {
	fiberoapi.Post(controller.FiberOapi, "/login", controller.Login, fiberoapi.OpenAPIOptions{
		Summary:     "Login user",
		Description: "Authenticate user and return a token",
		OperationID: "auth.login",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/logout", controller.Logout, fiberoapi.OpenAPIOptions{
		Summary:     "Logout user",
		Description: "End the current session; its token stops working",
		OperationID: "auth.logout",
		Tags:        []string{"Auth"},
	})

	fiberoapi.Post(controller.FiberOapi, "/session", controller.CreateSession, fiberoapi.OpenAPIOptions{
		Summary:     "Log in the web UI",
		Description: "Like /auth/login, but the token goes into an HttpOnly cookie (ogit_session) and is not returned. Also sets ogit_csrf: cookie-authenticated writes must send its value in X-CSRF-Token.",
		OperationID: "auth.createSession",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Delete(controller.FiberOapi, "/session", controller.DeleteSession, fiberoapi.OpenAPIOptions{
		Summary:     "Log out of the web UI",
		Description: "Ends the session and deletes the cookies",
		OperationID: "auth.deleteSession",
		Tags:        []string{"Auth"},
	})

	fiberoapi.Post(controller.FiberOapi, "/register", controller.Register, fiberoapi.OpenAPIOptions{
		Summary:     "Register user",
		Description: "Register a new user",
		OperationID: "auth.register",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/verify", controller.VerifyEmail, fiberoapi.OpenAPIOptions{
		Summary:     "Verify email",
		Description: "Consume the token from a verification email. The emailed link itself is GET /api/v1/auth/verify?token=..., which answers with an HTML page.",
		OperationID: "auth.verifyEmail",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/verify/resend", controller.ResendVerification, fiberoapi.OpenAPIOptions{
		Summary:     "Resend verification email",
		Description: "Sends a new link if the address belongs to an unverified account (at most one per minute; older links stop working). Always answers the same.",
		OperationID: "auth.resendVerification",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})
}
