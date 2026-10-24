package application

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

type TokenApp struct {
	Config    config.Config
	Logger    zerolog.Logger
	TokenPers domain.TokenPers
}

func NewTokenApp(config config.Config, logger zerolog.Logger, tokenPers domain.TokenPers) *TokenApp {
	return &TokenApp{
		Config:    config,
		Logger:    logger,
		TokenPers: tokenPers,
	}
}

// GetByNameAndToken retrieves a token by its name and token string, ensuring it is active and not expired.
func (c *TokenApp) GetByNameAndToken(name, tokenStr string) (*domain.Token, error) {
	logger := c.Logger.With().Str("component", "application.token.get_by_name_and_token").Logger()

	logger.Debug().Str("name", name).Msg("fetching token by name and token")

	token, err := c.TokenPers.GetByNameAndToken(name, HashToken(tokenStr))
	switch helperError.Catch(err) {
	case gorm.ErrRecordNotFound:
		logger.Warn().Str("name", name).Msg("token not found")
		return nil, fmt.Errorf("token not found")
	case nil:
		// No error, continue processing
	default:
		logger.Error().Err(err).Str("name", name).Msg("failed to fetch token by name and token")
		return nil, fmt.Errorf("failed to fetch token: %w", err)
	}

	if !token.Active {
		logger.Warn().Str("name", name).Msg("token inactive")
		return nil, fmt.Errorf("token inactive")
	}

	if token.ExpiresAt != nil && token.ExpiresAt.Before(time.Now()) {
		logger.Warn().Str("name", name).Msg("token expired")
		return nil, fmt.Errorf("token expired")
	}

	return token, nil
}

// tokenPrefix makes oGit tokens recognisable (e.g. by secret scanners).
const tokenPrefix = "ogit_"

// HashToken returns the value stored in the token column. Only the hash is
// persisted; the plaintext token is shown to the user once, at creation.
func HashToken(tokenStr string) string {
	sum := sha256.Sum256([]byte(tokenStr))
	return hex.EncodeToString(sum[:])
}

// grantableScopes are the scopes a user may put on a personal access token.
var grantableScopes = []domain.TokenScope{
	domain.ScopeReadRepository,
	domain.ScopeWriteRepository,
}

// CreateUserToken creates a personal access token for userId and returns the
// plaintext token, which is not stored and cannot be retrieved later.
func (c *TokenApp) CreateUserToken(userId, name, description string, scopes []domain.TokenScope, expiresAt *time.Time) (string, *domain.Token, error) {
	if len(scopes) == 0 {
		return "", nil, fmt.Errorf("at least one scope is required")
	}
	for _, s := range scopes {
		if !slices.Contains(grantableScopes, s) {
			return "", nil, fmt.Errorf("unsupported scope: %s", s)
		}
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return "", nil, fmt.Errorf("expiration must be in the future")
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, fmt.Errorf("failed to generate token: %w", err)
	}
	plaintext := tokenPrefix + hex.EncodeToString(secret)

	token := &domain.Token{
		Name:        name,
		Description: description,
		Token:       HashToken(plaintext),
		Type:        domain.TokenUser,
		UserId:      &userId,
		Active:      true,
		Scopes:      scopes,
		ExpiresAt:   expiresAt,
	}
	if err := c.Create(token); err != nil {
		return "", nil, err
	}

	return plaintext, token, nil
}

func (c *TokenApp) Create(token *domain.Token) error {
	logger := c.Logger.With().Str("component", "application.token.create").Logger()

	err := c.TokenPers.Create(token)
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		logger.Warn().Str("token_name", token.Description).Msg("token with the same name already exists")
		return fmt.Errorf("token with the same name already exists")
	} else if err != nil {
		logger.Error().Err(err).Str("token_id", token.Id).Str("token_name", token.Description).Msg("failed to create token")
		return err
	}

	return nil
}
