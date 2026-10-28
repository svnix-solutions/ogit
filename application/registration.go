package application

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/labbs/ogit/infrastructure/config"
)

var (
	// ErrRegistrationDisabled is returned when registration.enabled is false.
	ErrRegistrationDisabled = errors.New("registration is disabled")
	// ErrRegistrationRejected wraps a policy violation (domain, password).
	ErrRegistrationRejected = errors.New("registration rejected")
)

// checkRegistrationPolicy enforces the registration.* settings.
// registration.require_email_verification is not enforced: there is no
// email verification flow yet.
func checkRegistrationPolicy(cfg config.Config, email, password string) error {
	policy := cfg.Registration

	if !policy.Enabled {
		return ErrRegistrationDisabled
	}

	if len(policy.DomainWhitelist) > 0 {
		_, domain, ok := strings.Cut(email, "@")
		domain = strings.ToLower(strings.TrimSpace(domain))
		allowed := false
		for _, d := range policy.DomainWhitelist {
			if ok && domain == strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "@")) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: email domain is not allowed", ErrRegistrationRejected)
		}
	}

	return checkPasswordPolicy(cfg, password)
}

// checkPasswordPolicy enforces password_min_length and password_complexity.
// It also applies to admin-created accounts and password changes.
func checkPasswordPolicy(cfg config.Config, password string) error {
	policy := cfg.Registration

	if n := len([]rune(password)); n < policy.PasswordMinLength {
		return fmt.Errorf("%w: password must be at least %d characters", ErrRegistrationRejected, policy.PasswordMinLength)
	}

	if policy.PasswordComplexity {
		var upper, lower, digit, symbol bool
		for _, r := range password {
			switch {
			case unicode.IsUpper(r):
				upper = true
			case unicode.IsLower(r):
				lower = true
			case unicode.IsDigit(r):
				digit = true
			default:
				symbol = true
			}
		}
		if !upper || !lower || !digit || !symbol {
			return fmt.Errorf("%w: password must contain upper and lower case letters, a digit and a symbol", ErrRegistrationRejected)
		}
	}

	return nil
}
