package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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

// PasswordCost is bcrypt's cost for new password hashes. Tests lower it to
// bcrypt.MinCost before running: at the default, one hash takes half a
// second under the race detector, and every test user needs one.
var PasswordCost = bcrypt.DefaultCost

var (
	dummyHashOnce sync.Once
	dummyHash     []byte
)

// dummyPasswordHash is a bcrypt hash (same cost as real ones) to compare
// against when no user matches, so that the response takes as long.
func dummyPasswordHash() []byte {
	dummyHashOnce.Do(func() {
		dummyHash, _ = bcrypt.GenerateFromPassword([]byte("ogit-dummy-password"), PasswordCost)
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

	// TwoFactor asks accounts that have it for a second factor at login.
	TwoFactor *TwoFactorApp

	// ForkEdits returns the branches of a fork a user may push to as a
	// maintainer of pull requests from it (PullRequestApp.EditableBranches).
	ForkEdits func(fork *domain.Repository, userId string) []string
	// Actions resolves job tokens (GITHUB_TOKEN) for git; nil: none.
	Actions *ActionsApp
	// PasswordBlocked: password and passkey sign-in are off for user
	// (only sign-in providers, application/sso_sync.go); nil: never.
	PasswordBlocked func(user *domain.User) bool
	// SSOOnly: sign-in only through providers (no registration).
	SSOOnly func() bool
	// Directory tries the sign-in form's credentials on LDAP directories
	// first (application/sso_ldap.go); nil: none.
	Directory func(ctx context.Context, input, password, userAgent, ip string) (*SSOOutcome, bool, error)
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

	if c.Directory != nil {
		out, handled, err := c.Directory(ctx.UserContext(), email, password, ctx.Get("User-Agent"), ctx.IP())
		if err != nil {
			return nil, err
		}
		if handled {
			switch {
			case out.LinkToken != "":
				return &dtos.LoginResponse{LinkToken: out.LinkToken}, nil
			case out.Challenge != "":
				return &dtos.LoginResponse{TwoFactorRequired: true, Challenge: out.Challenge, Methods: out.Methods}, nil
			}
			return out.Login, nil
		}
	}

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
	if c.PasswordBlocked != nil && c.PasswordBlocked(user) {
		return nil, ErrPasswordLoginDisabled
	}

	if c.TwoFactor != nil {
		on, err := c.TwoFactor.Enabled(user.Id)
		if err != nil {
			return nil, err
		}
		if on {
			challenge, err := c.TwoFactor.StartLogin(user, ctx.Get("User-Agent"), ctx.IP())
			if err != nil {
				return nil, err
			}
			methods, err := c.TwoFactor.Methods(user.Id)
			if err != nil {
				return nil, err
			}
			return &dtos.LoginResponse{TwoFactorRequired: true, Challenge: challenge, Methods: methods}, nil
		}
	}
	return c.newSession(user.Id, ctx.Get("User-Agent"), ctx.IP())
}

// CompleteTwoFactorLogin finishes a login with its second factor.
func (c *AuthApp) CompleteTwoFactorLogin(challenge, code string, key *KeyAssertion) (*dtos.LoginResponse, error) {
	if c.TwoFactor == nil {
		return nil, ErrChallengeExpired
	}
	user, ch, err := c.TwoFactor.CompleteLogin(challenge, code, key)
	if err != nil {
		return nil, err
	}
	return c.newSession(user.Id, ch.UserAgent, ch.IpAddress)
}

// PasskeySignIn signs in with a passkey (no password: the passkey
// verified the user).
func (c *AuthApp) PasskeySignIn(key *KeyAssertion, userAgent, ip string) (*dtos.LoginResponse, error) {
	if c.TwoFactor == nil {
		return nil, ErrWebAuthnUnavailable
	}
	user, err := c.TwoFactor.FinishPasskeyLogin(key)
	if err != nil {
		return nil, err
	}
	if c.Config.Registration.RequireEmailVerification && user.EmailVerifiedAt == nil {
		return nil, ErrEmailNotVerified
	}
	if c.PasswordBlocked != nil && c.PasswordBlocked(user) {
		return nil, ErrPasswordLoginDisabled
	}
	return c.newSession(user.Id, userAgent, ip)
}

// newSession creates a session and its token. A new session starts in
// sudo mode (the password, and code, were just checked).
func (c *AuthApp) newSession(userId, userAgent, ip string) (*dtos.LoginResponse, error) {
	return c.newSessionVia(userId, userAgent, ip, nil)
}

// newSessionVia is newSession for a sign-in through a provider whose MFA
// is trusted (ssoProvider: the session counts as two-factor).
func (c *AuthApp) newSessionVia(userId, userAgent, ip string, ssoProvider *string) (*dtos.LoginResponse, error) {
	logger := c.Logger.With().Str("component", "application.auth.authenticate").Logger()
	sudo := time.Now().Add(SudoWindow)
	session := &domain.Session{
		UserId:      userId,
		UserAgent:   userAgent,
		IpAddress:   ip,
		ExpiresAt:   time.Now().Add(time.Minute * time.Duration(c.Config.Session.ExpirationMinutes)),
		SudoUntil:   &sudo,
		SSOProvider: ssoProvider,
	}

	err := c.SessionApp.Create(session)
	if err != nil {
		logger.Error().Err(err).Str("user_id", userId).Msg("failed to create session")
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	accessToken, err := tokenutil.CreateAccessToken(userId, session.Id, c.Config)
	if err != nil {
		logger.Error().Err(err).Str("user_id", userId).Str("session_id", session.Id).Msg("failed to create access token")
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
// AuthorizeGit checks a git operation on the repository at repoPath
// (<namespace>/<slug>).
func (c *AuthApp) AuthorizeGit(repoPath, operation string, creds *GitCredentials) (*domain.Repository, error) {
	repo, _, err := c.AuthorizeGitAs(repoPath, operation, creds)
	return repo, err
}

// AuthorizeGitAs is AuthorizeGit, also returning who the caller acts as
// (for push events): the token's user, or the repository token's name.
func (c *AuthApp) AuthorizeGitAs(repoPath, operation string, creds *GitCredentials) (*domain.Repository, Pusher, error) {
	if creds != nil && IsJobToken(creds.Token) {
		return c.authorizeJobToken(repoPath, operation, creds.Token)
	}
	repo, authToken, only, err := c.authorizeGit(repoPath, operation, creds)
	if err != nil || authToken == nil {
		return repo, Pusher{}, err
	}
	if authToken.Type == domain.TokenUser && authToken.UserId != nil {
		return repo, Pusher{UserId: *authToken.UserId, Name: authToken.User.Username, OnlyBranches: only}, nil
	}
	return repo, Pusher{Name: "token " + authToken.Name}, nil
}

// authorizeJobToken: an Actions job token reads its run's repository
// while the run runs (whatever the token's user name), and pushes to it
// with `permissions: contents: write`.
func (c *AuthApp) authorizeJobToken(repoPath, operation, token string) (*domain.Repository, Pusher, error) {
	run, err := c.Actions.JobTokenRun(token)
	if err != nil {
		return nil, Pusher{}, ErrGitUnauthenticated
	}
	path, wiki := domain.SplitWikiPath(repoPath)
	repo, err := c.RepositoryApp.GetRepository(nil, &path)
	if err != nil || repo.Id != run.RepositoryId || (wiki && repo.WikiAccess == domain.WikiOff) {
		return nil, Pusher{}, ErrGitNotFound
	}
	if operation != "git-upload-pack" {
		if !hasPermission(run, "contents", "write") {
			return nil, Pusher{}, ErrGitForbidden
		}
		if repo.IsArchived {
			return nil, Pusher{}, ErrGitArchived
		}
		if repo.IsMirror {
			return nil, Pusher{}, ErrGitMirror
		}
	}
	if wiki {
		if repo, err = c.RepositoryApp.wikiFor(repo, operation != "git-upload-pack"); err != nil {
			return nil, Pusher{}, err
		}
	}
	return repo, Pusher{Name: "github-actions[bot]", JobToken: true}, nil
}

// wikiGitAccess decides a user's git access to repo's wiki: readers of
// the repository read it; writers edit it, or every reader when the
// repository says so (application/wiki_app.go).
func (c *AuthApp) wikiGitAccess(repo *domain.Repository, userId string, write bool) error {
	canRead, err := c.RepositoryApp.CanUserAccess(repo.Id, userId, "read")
	if err != nil {
		return err
	}
	if !canRead {
		return ErrGitNotFound
	}
	if !write || repo.WikiAccess == domain.WikiReaders {
		return nil
	}
	canWrite, err := c.RepositoryApp.CanUserAccess(repo.Id, userId, "write")
	if err != nil {
		return err
	}
	if !canWrite {
		return ErrGitForbidden
	}
	return nil
}

// userGitAccess decides a user's git access to repo. A user who can't
// read or write it may still be a maintainer of open pull requests from
// this fork that allow edits: they then read it, and push only those pull
// requests' source branches (the returned list).
func (c *AuthApp) userGitAccess(repo *domain.Repository, userId string, write bool) ([]string, error) {
	canRead, err := c.RepositoryApp.CanUserAccess(repo.Id, userId, "read")
	if err != nil {
		return nil, err
	}
	canWrite := false
	if write && canRead {
		if canWrite, err = c.RepositoryApp.CanUserAccess(repo.Id, userId, "write"); err != nil {
			return nil, err
		}
	}
	if canRead && (!write || canWrite) {
		return nil, nil
	}
	if repo.ForkOfId != nil && c.ForkEdits != nil {
		if branches := c.ForkEdits(repo, userId); len(branches) > 0 {
			return branches, nil
		}
	}
	if !canRead {
		return nil, ErrGitNotFound
	}
	return nil, ErrGitForbidden
}

func (c *AuthApp) authorizeGit(repoPath, operation string, creds *GitCredentials) (*domain.Repository, *domain.Token, []string, error) {
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
		return nil, nil, nil, fmt.Errorf("invalid git operation: %s", operation)
	}

	path, wiki := domain.SplitWikiPath(repoPath)
	repo, repoErr := c.RepositoryApp.GetRepository(nil, &path)
	if repoErr == nil && wiki && repo.WikiAccess == domain.WikiOff {
		repoErr = ErrGitNotFound
	}
	var only []string // set when the user may only push some branches

	if creds == nil {
		if repoErr == nil && !repo.IsPrivate && gitOperation == "read" {
			if wiki {
				repo, repoErr = c.RepositoryApp.wikiFor(repo, false)
			}
			return repo, nil, nil, repoErr
		}
		return nil, nil, nil, ErrGitUnauthenticated
	}

	authToken, err := c.TokenApp.GetByNameAndToken(creds.Name, creds.Token)
	if err != nil {
		logger.Warn().Err(err).Str("name", creds.Name).Msg("invalid git token")
		return nil, nil, nil, ErrGitUnauthenticated
	}

	if repoErr != nil {
		return nil, nil, nil, ErrGitNotFound
	}

	switch authToken.Type {
	case domain.TokenUser:
		if authToken.UserId == nil || !authToken.User.Active {
			logger.Warn().Str("token_id", authToken.Id).Msg("git token belongs to an inactive user")
			return nil, nil, nil, ErrGitUnauthenticated
		}
		if must, err := c.TwoFactor.MustEnroll(*authToken.UserId); err != nil {
			return nil, nil, nil, err
		} else if must {
			return nil, nil, nil, ErrGitTwoFactorRequired
		}

		if wiki {
			err = c.wikiGitAccess(repo, *authToken.UserId, gitOperation == "write")
		} else {
			only, err = c.userGitAccess(repo, *authToken.UserId, gitOperation == "write")
		}
		if err != nil {
			return nil, nil, nil, err
		}

	case domain.TokenRepo:
		if authToken.RepositoryId == nil || *authToken.RepositoryId != repo.Id {
			return nil, nil, nil, ErrGitNotFound
		}

	default:
		return nil, nil, nil, ErrGitForbidden
	}

	if !slices.Contains(authToken.Scopes, requiredScope) {
		return nil, nil, nil, ErrGitForbidden
	}
	c.TokenApp.Touch(authToken)

	if gitOperation == "write" && repo.IsArchived {
		return nil, nil, nil, ErrGitArchived
	}
	if gitOperation == "write" && repo.IsMirror {
		return nil, nil, nil, ErrGitMirror
	}
	if wiki {
		if repo, err = c.RepositoryApp.wikiFor(repo, gitOperation == "write"); err != nil {
			return nil, nil, nil, err
		}
	}

	return repo, authToken, only, nil
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
	hash, err := bcrypt.GenerateFromPassword([]byte(next), PasswordCost)
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

	if c.SSOOnly != nil && c.SSOOnly() {
		return ErrRegistrationDisabled
	}
	if err := checkRegistrationPolicy(c.Config, email, password); err != nil {
		return err
	}
	// The username is also the user's namespace (see namespace.go).
	switch err := checkNewNamespace(c.RepositoryApp.UserPers, c.RepositoryApp.GroupPers, username); err {
	case nil:
	case domain.ErrInvalidNamespaceName:
		return fmt.Errorf("%w: username %s", ErrRegistrationRejected, strings.TrimPrefix(err.Error(), "name "))
	case ErrNamespaceTaken:
		return fmt.Errorf("%w: username is already taken", ErrRegistrationRejected)
	default:
		return err
	}

	// check if the email is already in use
	_, err := c.UserApp.GetByEmail(email)
	if err == nil {
		return fmt.Errorf("email is already in use")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), PasswordCost)
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
