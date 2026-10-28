package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/rs/zerolog"
)

// MinSessionSecretLength is the minimum HS256 key size (RFC 7518 §3.2).
const MinSessionSecretLength = 32

// ValidateEncryptionKey: security.encryption_key is optional (two-factor
// authentication with an app needs it), but at least 32 bytes when set.
func (c *Config) ValidateEncryptionKey() error {
	if k := c.Security.EncryptionKey; k != "" && len(k) < MinSessionSecretLength {
		return fmt.Errorf("security.encryption_key must be at least %d bytes, got %d", MinSessionSecretLength, len(k))
	}
	return nil
}

// EnsureSessionSecret validates the session secret. An unset secret is
// replaced by a random one (with a warning, since sessions are then
// invalidated on every restart); a secret shorter than
// MinSessionSecretLength is rejected.
func (c *Config) EnsureSessionSecret(logger zerolog.Logger) error {
	if c.Session.SecretKey == "" {
		key := make([]byte, MinSessionSecretLength)
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("failed to generate session secret: %w", err)
		}
		c.Session.SecretKey = hex.EncodeToString(key)
		logger.Warn().Str("event", "config.session.secret_generated").
			Msg("session.secret_key is not set; using a random key, sessions will not survive a restart")
		return nil
	}

	if len(c.Session.SecretKey) < MinSessionSecretLength {
		return fmt.Errorf("session.secret_key must be at least %d bytes, got %d", MinSessionSecretLength, len(c.Session.SecretKey))
	}
	return nil
}
