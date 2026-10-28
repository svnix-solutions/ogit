package application

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
)

// Email verification (registration.require_email_verification):
//   - registration sends a single-use link valid verification_ttl_hours;
//     if sending fails the account is kept and the user can resend;
//   - unverified users cannot log in while verification is required;
//   - resending answers the same whatever the email, sends at most one
//     email per account per resendInterval and invalidates older links;
//   - accounts created while verification is off are verified.

var (
	ErrEmailNotVerified         = errors.New("email address not verified")
	ErrInvalidVerificationToken = errors.New("invalid or expired verification link")
)

const resendInterval = time.Minute

// VerifyEmail consumes a verification token.
func (c *AuthApp) VerifyEmail(token string) error {
	v, err := c.VerificationPers.GetByTokenHash(HashToken(token))
	if isNotFound(err) {
		return ErrInvalidVerificationToken
	} else if err != nil {
		return err
	}
	if time.Now().After(v.ExpiresAt) {
		return ErrInvalidVerificationToken
	}

	if err := c.UserApp.UserPres.MarkEmailVerified(v.UserId, time.Now()); err != nil {
		return err
	}
	return c.VerificationPers.DeleteByUserId(v.UserId)
}

// ResendVerification sends a new link if email belongs to an unverified
// account. It returns nil in every other case so that callers cannot tell
// which emails are registered; delivery errors are only logged.
func (c *AuthApp) ResendVerification(email string) error {
	logger := c.Logger.With().Str("component", "application.auth.resend_verification").Logger()

	if !c.Config.Registration.RequireEmailVerification {
		return nil
	}
	user, err := c.UserApp.GetByEmail(email)
	if err != nil || user.EmailVerifiedAt != nil {
		return nil
	}

	if pending, err := c.VerificationPers.GetByUserId(user.Id); err == nil && time.Since(pending.CreatedAt) < resendInterval {
		logger.Info().Str("user_id", user.Id).Msg("verification email recently sent, not resending")
		return nil
	}

	if err := c.sendVerification(user); err != nil {
		logger.Error().Err(err).Str("user_id", user.Id).Msg("failed to send verification email")
	}
	return nil
}

// sendVerification stores a new token for user (replacing any pending one)
// and emails the link.
func (c *AuthApp) sendVerification(user *domain.User) error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	ttl := time.Duration(c.Config.Registration.VerificationTTLHours) * time.Hour

	if err := c.VerificationPers.Replace(&domain.EmailVerification{
		Id:        utils.UUIDv4(),
		UserId:    user.Id,
		TokenHash: HashToken(token),
		ExpiresAt: time.Now().Add(ttl),
		CreatedAt: time.Now(),
	}); err != nil {
		return err
	}

	link := verificationURL(c.Config.Registration.VerificationURLTemplate, c.Config.Server.PublicURL, token)
	body := fmt.Sprintf("Hello %s,\n\n"+
		"Confirm your email address for oGit by opening this link within %d hours:\n\n"+
		"%s\n\n"+
		"If you did not create an account, you can ignore this email.\n",
		user.Username, c.Config.Registration.VerificationTTLHours, link)

	return c.Mailer.Send(user.Email, "Verify your email address", body)
}

func verificationURL(template, publicURL, token string) string {
	return strings.NewReplacer(
		"{public_url}", strings.TrimRight(publicURL, "/"),
		"{token}", url.QueryEscape(token),
	).Replace(template)
}
