package auth

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
	AuthApp   *application.AuthApp
	Limits    *ratelimit.Guard
}

func SetupAuthRouter(controller Controller) {
	fiberoapi.Post(controller.FiberOapi, "/login", controller.Login, fiberoapi.OpenAPIOptions{
		Summary:     "Login user",
		Description: "Authenticate user and return a token",
		OperationID: "auth.login",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/login/2fa", controller.LoginTwoFactor, fiberoapi.OpenAPIOptions{
		Summary:     "Complete a login with two-factor authentication",
		Description: "When /auth/login answers two_factor_required, send its challenge with a code from the authenticator app or a recovery code. The challenge lasts 5 minutes and 5 attempts; wrong codes count as failed logins.",
		OperationID: "auth.login2fa",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/session/2fa", controller.SessionTwoFactor, fiberoapi.OpenAPIOptions{
		Summary:     "Complete a web UI login with two-factor authentication",
		Description: "Like /auth/login/2fa, for /auth/session: the token goes into the session cookie.",
		OperationID: "auth.session2fa",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/login/2fa/webauthn", controller.LoginKeyOptions, fiberoapi.OpenAPIOptions{
		Summary:     "Use a security key to complete a login",
		Description: "Returns WebAuthn assertion options for the login's challenge; send the browser's answer to /auth/login/2fa (or /auth/session/2fa) as credential, with ceremony.",
		OperationID: "auth.login2faWebauthn",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/passkey", controller.PasskeyOptions, fiberoapi.OpenAPIOptions{
		Summary:     "Start a passkey sign-in",
		Description: "WebAuthn options for any passkey of this site (no email or password). Send the browser's answer to /auth/passkey/finish or /auth/session/passkey.",
		OperationID: "auth.passkey",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/passkey/finish", controller.PasskeyLogin, fiberoapi.OpenAPIOptions{
		Summary:     "Sign in with a passkey",
		Description: "The passkey must verify the user (PIN or biometrics); it counts as two factors. Returns a token like /auth/login. Refused answers count as failed logins.",
		OperationID: "auth.passkeyFinish",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/session/passkey", controller.SessionPasskey, fiberoapi.OpenAPIOptions{
		Summary:     "Sign in to the web UI with a passkey",
		Description: "Like /auth/passkey/finish, but the token goes into the session cookie.",
		OperationID: "auth.sessionPasskey",
		Tags:        []string{"Auth"},
		Security:    "disabled",
	})

	fiberoapi.Post(controller.FiberOapi, "/sudo/webauthn", controller.SudoKeyOptions, fiberoapi.OpenAPIOptions{
		Summary:     "Use a security key to confirm it's you",
		Description: "WebAuthn assertion options; send the answer to /auth/sudo as credential, with ceremony.",
		OperationID: "auth.sudoWebauthn",
		Tags:        []string{"Auth"},
	})

	fiberoapi.Post(controller.FiberOapi, "/sudo", controller.Sudo, fiberoapi.OpenAPIOptions{
		Summary:     "Confirm it's you",
		Description: "Sensitive actions (two-factor changes, access tokens, SSH and deploy keys, repository tokens) need a session confirmed in the last 10 minutes; they answer 403 SUDO_REQUIRED otherwise. Signing in counts. Send code (with two-factor authentication) or password (without).",
		OperationID: "auth.sudo",
		Tags:        []string{"Auth"},
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
