package application

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/tokenutil"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/labbs/ogit/interfaces/http/v1/auth/dtos"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrInvalidCredentials is returned for an unknown email and for a
	// wrong password alike.
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrAccountDisabled    = errors.New("account is disabled")
)

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

// dummyPasswordHash is a bcrypt hash (same cost as real ones) to compare
// against when no user matches, so that the response takes as long.
func dummyPasswordHash() []byte {
	dummyHashOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("ogit-dummy-password"), bcrypt.DefaultCost)
	})
	return dummyHash
}

type AuthApp struct {
	Config        config.Config
	Logger        zerolog.Logger
	UserApp       UserApp
	SessionApp    SessionApp
	TokenApp      TokenApp
	RepositoryApp RepositoryApp

	// Email verification
	Mailer           mail.Sender
	VerificationPers domain.EmailVerificationPers
}

func NewAuthApp(config config.Config, logger zerolog.Logger, userApp UserApp, sessionApp SessionApp, tokenApp TokenApp, repositoryApp RepositoryApp, mailer mail.Sender, verificationPers domain.EmailVerificationPers) *AuthApp {
	return &AuthApp{
		Config:           config,
		Logger:           logger,
		UserApp:          userApp,
		SessionApp:       sessionApp,
		TokenApp:         tokenApp,
		RepositoryApp:    repositoryApp,
		Mailer:           mailer,
		VerificationPers: verificationPers,
	}
}

func (c *AuthApp) Authenticate(email, password string, ctx *fiber.Ctx) (*dtos.LoginResponse, error) {
	logger := c.Logger.With().Str("component", "application.auth.authenticate").Logger()

	// An unknown email and a wrong password must be indistinguishable, in
	// the answer and in timing: compare against a dummy hash when there is no
	// user, so both paths pay for one bcrypt comparison.
	user, err := c.UserApp.GetByEmail(email)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(password))
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		logger.Warn().Str("user_id", user.Id).Msg("invalid password attempt")
		return nil, ErrInvalidCredentials
	}

	// Account state is checked after the password, so only the account
	// holder learns it.
	if !user.Active {
		logger.Warn().Str("user_id", user.Id).Msg("login attempt on disabled account")
		return nil, ErrAccountDisabled
	}
	if c.Config.Registration.RequireEmailVerification && user.EmailVerifiedAt == nil {
		return nil, ErrEmailNotVerified
	}

	session := &domain.Session{
		UserId:    user.Id,
		UserAgent: ctx.Get("User-Agent"),
		IpAddress: ctx.IP(),
		ExpiresAt: time.Now().Add(time.Minute * time.Duration(c.Config.Session.ExpirationMinutes)),
	}

	err = c.SessionApp.Create(session)
	if err != nil {
		logger.Error().Err(err).Str("user_id", user.Id).Msg("failed to create session")
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	accessToken, err := tokenutil.CreateAccessToken(user.Id, session.Id, c.Config)
	if err != nil {
		logger.Error().Err(err).Str("user_id", user.Id).Str("session_id", session.Id).Msg("failed to create access token")
		return nil, fmt.Errorf("failed to create access token: %w", err)
	}

	return &dtos.LoginResponse{
		Token: accessToken,
	}, nil
}

// Git authorization outcomes. The HTTP layer maps them to status codes:
// ErrGitUnauthenticated → 401 (git prompts for credentials),
// ErrGitNotFound → 404, ErrGitForbidden → 403.
var (
	ErrGitUnauthenticated = errors.New("authentication required")
	ErrGitNotFound        = errors.New("repository not found")
	ErrGitForbidden       = errors.New("permission denied")
)

// GitCredentials are the HTTP Basic credentials sent by a git client:
// the token name as username and the token itself as password.
type GitCredentials struct {
	Name  string
	Token string
}

// AuthorizeGit decides whether a git operation ("git-upload-pack" for
// clone/fetch, "git-receive-pack" for push) on the repository with the given
// slug is allowed. creds is nil for anonymous requests, which may only read
// public repositories. Private repositories the caller cannot read are
// reported as not found so that their existence is not disclosed.
func (c *AuthApp) AuthorizeGit(repoSlug, operation string, creds *GitCredentials) error {
	logger := c.Logger.With().Str("component", "application.auth.authorize_git").Logger()

	var requiredScope domain.TokenScope
	var gitOperation string
	switch operation {
	case "git-upload-pack": // git clone, git fetch
		requiredScope = domain.ScopeReadRepository
		gitOperation = "read"
	case "git-receive-pack": // git push
		requiredScope = domain.ScopeWriteRepository
		gitOperation = "write"
	default:
		return fmt.Errorf("invalid git operation: %s", operation)
	}

	repo, repoErr := c.RepositoryApp.GetRepository(nil, &repoSlug)

	if creds == nil {
		if repoErr == nil && !repo.IsPrivate && gitOperation == "read" {
			return nil
		}
		return ErrGitUnauthenticated
	}

	authToken, err := c.TokenApp.GetByNameAndToken(creds.Name, creds.Token)
	if err != nil {
		logger.Warn().Err(err).Str("name", creds.Name).Msg("invalid git token")
		return ErrGitUnauthenticated
	}

	if repoErr != nil {
		return ErrGitNotFound
	}

	switch authToken.Type {
	case domain.TokenUser:
		if authToken.UserId == nil || !authToken.User.Active {
			logger.Warn().Str("token_id", authToken.Id).Msg("git token belongs to an inactive user")
			return ErrGitUnauthenticated
		}

		canRead, err := c.RepositoryApp.CanUserAccess(repo.Id, *authToken.UserId, "read")
		if err != nil {
			logger.Error().Err(err).Str("repo_id", repo.Id).Msg("failed to check repository access")
			return err
		}
		if !canRead {
			return ErrGitNotFound
		}
		if gitOperation == "write" {
			canWrite, err := c.RepositoryApp.CanUserAccess(repo.Id, *authToken.UserId, "write")
			if err != nil {
				logger.Error().Err(err).Str("repo_id", repo.Id).Msg("failed to check repository access")
				return err
			}
			if !canWrite {
				return ErrGitForbidden
			}
		}

	case domain.TokenRepo:
		if authToken.RepositoryId == nil || *authToken.RepositoryId != repo.Id {
			return ErrGitNotFound
		}

	default:
		return ErrGitForbidden
	}

	if !slices.Contains(authToken.Scopes, requiredScope) {
		return ErrGitForbidden
	}

	return nil
}

// ChangePassword replaces the user's password after checking the current
// one. The user's other sessions end; sessionId (the caller's) is kept.
func (c *AuthApp) ChangePassword(userId, sessionId, current, next string) error {
	user, err := c.UserApp.GetById(userId)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(current)); err != nil {
		return ErrInvalidCredentials
	}
	if err := checkPasswordPolicy(c.Config, next); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := c.UserApp.UserPres.SetPassword(userId, string(hash)); err != nil {
		return err
	}
	return c.SessionApp.SessionPers.DeleteByUserId(userId, sessionId)
}

// Logout ends the session identified by sessionId.
func (c *AuthApp) Logout(sessionId string) error {
	return c.SessionApp.Delete(sessionId)
}

func (c *AuthApp) Register(username, email, password string) error {
	logger := c.Logger.With().Str("component", "application.auth.register").Logger()

	if err := checkRegistrationPolicy(c.Config, email, password); err != nil {
		return err
	}

	// check if the email is already in use
	_, err := c.UserApp.GetByEmail(email)
	if err == nil {
		return fmt.Errorf("email is already in use")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		logger.Error().Err(err).Str("email", email).Msg("failed to hash password")
		return fmt.Errorf("failed to hash password: %w", err)
	}

	user := domain.User{
		Id:       utils.UUIDv4(),
		Username: username,
		Email:    email,
		Password: string(hashedPassword),
		Active:   true,
	}
	requireVerification := c.Config.Registration.RequireEmailVerification
	if !requireVerification {
		now := time.Now()
		user.EmailVerifiedAt = &now
	}

	err = c.UserApp.Create(user)
	if err != nil {
		logger.Error().Err(err).Str("email", email).Msg("failed to create user")
		return fmt.Errorf("failed to create user: %w", err)
	}

	if requireVerification {
		// The account is kept if sending fails; the user can resend.
		if err := c.sendVerification(&user); err != nil {
			logger.Error().Err(err).Str("user_id", user.Id).Msg("failed to send verification email")
		}
	}

	return nil
}
