package optauth

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/ratelimit"
)

// ErrTooManyFailures: this IP sent too many bad tokens; Retry is when to
// try again (seconds).
type ErrTooManyFailures struct{ Retry string }

func (e *ErrTooManyFailures) Error() string {
	return "too many failed authentication attempts; try again in " + e.Retry + " seconds"
}

// Token returns the personal access token a request presents, if any:
// "token <pat>" (GitHub's form), "Bearer ogit_...", or Basic with the token
// as the password (any username).
func Token(ctx *fiber.Ctx) string {
	h := ctx.Get(fiber.HeaderAuthorization)
	scheme, value, _ := strings.Cut(h, " ")
	value = strings.TrimSpace(value)
	switch strings.ToLower(scheme) {
	case "token":
		return value
	case "bearer":
		if strings.HasPrefix(value, "ogit_") {
			return value
		}
	case "basic":
		if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
			if _, pass, ok := strings.Cut(string(decoded), ":"); ok {
				return pass
			}
		}
	}
	return ""
}

// Caller identifies a request that may use a session or a personal access
// token. Without either it is anonymous; an invalid session is anonymous
// too, but an invalid token is application.ErrInvalidToken (and counts like
// a failed login for the IP).
func Caller(ctx *fiber.Ctx, sessions *application.SessionApp, tokens *application.TokenApp, limits *ratelimit.Guard) (application.StatusCaller, error) {
	if pat := Token(ctx); pat != "" {
		ip := limits.ClientIP(ctx)
		if r, blocked := limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
			return application.StatusCaller{}, &ErrTooManyFailures{Retry: ratelimit.RetryAfter(r)}
		}
		token, err := tokens.Authenticate(pat)
		if errors.Is(err, application.ErrInvalidToken) {
			limits.LoginFailed(ctx.UserContext(), ip, "")
		}
		if err != nil {
			return application.StatusCaller{}, err
		}
		if token.Type == domain.TokenRepo {
			return application.StatusCaller{UserId: domain.RepoTokenPrincipal(*token.RepositoryId), Token: token}, nil
		}
		return application.StatusCaller{UserId: *token.UserId, Token: token}, nil
	}
	return application.StatusCaller{UserId: UserID(ctx, sessions)}, nil
}

// Reader returns who reads a repository-scoped API route: the session's
// user, or a repository token's principal (a reader of its repository
// only) when the token has read:repository; "" is anonymous. Only routes
// under /repository/:identifier use it: elsewhere tokens are not readers.
func Reader(ctx *fiber.Ctx, sessions *application.SessionApp, tokens *application.TokenApp) string {
	if pat := Token(ctx); pat != "" && tokens != nil {
		token, err := tokens.Authenticate(pat)
		if err != nil || token.Type != domain.TokenRepo || !slices.Contains(token.Scopes, domain.ScopeReadRepository) {
			return ""
		}
		return domain.RepoTokenPrincipal(*token.RepositoryId)
	}
	return UserID(ctx, sessions)
}
