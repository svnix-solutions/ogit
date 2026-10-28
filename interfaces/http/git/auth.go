package git

import (
	"encoding/base64"
	"errors"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/repopath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
)

// storageKeyLocal is the request local holding the authorized repository's
// storage key.
const (
	storageKeyLocal = "git_storage_key"
	repositoryLocal = "git_repository"
	pusherLocal     = "git_pusher"
)

// Authorize returns a middleware that checks the caller may run the git
// service on the repository in the URL. suffix is the route suffix used to
// extract the repository ("/info/refs", "/git-upload-pack", ...). service is
// the git service for the route, or "" to read it from the ?service= query
// (info/refs).
func (ctrl *Controller) Authorize(suffix, service string) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		logger := ctrl.Logger.With().Str("component", "http.git.authorize").Logger()

		op := service
		if op == "" {
			op = ctx.Query("service")
		}
		if op != "git-upload-pack" && op != "git-receive-pack" {
			return ctx.Status(fiber.StatusBadRequest).SendString("invalid service parameter")
		}

		repoPath := helpergit.ExtractRepoPathFromURL(ctx.Path(), suffix)
		if repoPath == "" {
			return ctx.SendStatus(fiber.StatusNotFound)
		}
		path := strings.TrimSuffix(repoPath, ".git") // <namespace>/<repo>
		if nested, ok := repopath.Nested(ctx); ok {
			path = nested // a sub-group's repository
		}

		// Missing or malformed credentials are treated as anonymous.
		creds := BasicCredentials(ctx.Get(fiber.HeaderAuthorization))
		ip := ctrl.Limits.ClientIP(ctx)
		if creds != nil {
			// Token guessing counts like failed logins.
			if r, blocked := ctrl.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
				ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
				return ctx.Status(fiber.StatusTooManyRequests).SendString("too many failed authentication attempts; try again later")
			}
		}

		repo, pusher, err := ctrl.AuthApp.AuthorizeGitAs(path, op, creds)
		if creds != nil && errors.Is(err, application.ErrGitUnauthenticated) {
			ctrl.Limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		if err == nil {
			// Operations per token, or per IP without credentials.
			rule, key := ctrl.Limits.Rules.GitAnonymous, "git:ip:"+ip
			if creds != nil {
				rule, key = ctrl.Limits.Rules.GitUser, "git:token:"+application.HashToken(creds.Token)[:16]
			}
			if r := ctrl.Limits.Allow(ctx.UserContext(), rule, key); !r.Allowed {
				ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
				return ctx.Status(fiber.StatusTooManyRequests).SendString("too many git requests; try again later")
			}
		}
		switch {
		case err == nil:
			// The handlers serve the repository that was authorized (also when
			// reached through a former path), from its storage key.
			ctx.Locals(storageKeyLocal, repo.StorageKey)
			ctx.Locals(repositoryLocal, repo)
			ctx.Locals(pusherLocal, pusher)
			return ctx.Next()
		case errors.Is(err, application.ErrGitArchived):
			return ctx.Status(fiber.StatusForbidden).SendString("repository is archived (read-only)")
		case errors.Is(err, application.ErrGitMirror):
			return ctx.Status(fiber.StatusForbidden).SendString(err.Error())
		case errors.Is(err, application.ErrGitUnauthenticated):
			ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit"`)
			return ctx.Status(fiber.StatusUnauthorized).SendString(err.Error())
		case errors.Is(err, application.ErrGitNotFound):
			return ctx.Status(fiber.StatusNotFound).SendString(err.Error())
		case errors.Is(err, application.ErrGitForbidden):
			return ctx.Status(fiber.StatusForbidden).SendString(err.Error())
		default:
			logger.Error().Err(err).Str("repo", path).Str("service", op).Msg("git authorization failed")
			return ctx.Status(fiber.StatusInternalServerError).SendString("authorization failed")
		}
	}
}

// basicCredentials parses an HTTP Basic Authorization header, returning nil
// if it is absent or malformed.
// BasicCredentials parses "Authorization: Basic" (token name and token), or nil.
func BasicCredentials(header string) *application.GitCredentials {
	const prefix = "Basic "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(header[len(prefix):])
	if err != nil {
		return nil
	}
	name, token, ok := strings.Cut(string(decoded), ":")
	if !ok || name == "" || token == "" {
		return nil
	}
	return &application.GitCredentials{Name: name, Token: token}
}
