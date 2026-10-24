package git

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
)

// storageKeyLocal is the request local holding the authorized repository's
// storage key.
const storageKeyLocal = "git_storage_key"

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
		slug := strings.TrimSuffix(repoPath, ".git")

		// Missing or malformed credentials are treated as anonymous.
		creds := basicCredentials(ctx.Get(fiber.HeaderAuthorization))

		repo, err := ctrl.AuthApp.AuthorizeGit(slug, op, creds)
		switch {
		case err == nil:
			// The handlers serve the repository that was authorized (also when
			// reached through a former slug), from its storage key.
			ctx.Locals(storageKeyLocal, repo.StorageKey)
			return ctx.Next()
		case errors.Is(err, application.ErrGitArchived):
			return ctx.Status(fiber.StatusForbidden).SendString("repository is archived (read-only)")
		case errors.Is(err, application.ErrGitUnauthenticated):
			ctx.Set(fiber.HeaderWWWAuthenticate, `Basic realm="ogit"`)
			return ctx.Status(fiber.StatusUnauthorized).SendString(err.Error())
		case errors.Is(err, application.ErrGitNotFound):
			return ctx.Status(fiber.StatusNotFound).SendString(err.Error())
		case errors.Is(err, application.ErrGitForbidden):
			return ctx.Status(fiber.StatusForbidden).SendString(err.Error())
		default:
			logger.Error().Err(err).Str("repo", slug).Str("service", op).Msg("git authorization failed")
			return ctx.Status(fiber.StatusInternalServerError).SendString("authorization failed")
		}
	}
}

// basicCredentials parses an HTTP Basic Authorization header, returning nil
// if it is absent or malformed.
func basicCredentials(header string) *application.GitCredentials {
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
