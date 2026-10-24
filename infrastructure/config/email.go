package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"strings"
)

// DefaultVerificationURLTemplate points verification links at the API, which
// answers with a small confirmation page.
const DefaultVerificationURLTemplate = "{public_url}/api/v1/auth/verify?token={token}"

// ValidateEmailVerification checks the settings email verification needs.
// It is a no-op when verification is disabled.
func (c *Config) ValidateEmailVerification() error {
	if !c.Registration.RequireEmailVerification {
		return nil
	}

	var missing []string
	if c.SMTP.Host == "" {
		missing = append(missing, "smtp.host")
	}
	if c.SMTP.From == "" {
		missing = append(missing, "smtp.from")
	}
	if c.Server.PublicURL == "" {
		missing = append(missing, "http.public_url")
	}
	if len(missing) > 0 {
		return fmt.Errorf("registration.require_email_verification needs %s", strings.Join(missing, ", "))
	}

	if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
		return fmt.Errorf("smtp.from is not a valid address: %w", err)
	}
	switch c.SMTP.TLS {
	case "starttls", "tls", "none":
	default:
		return fmt.Errorf("smtp.tls must be starttls, tls or none, got %q", c.SMTP.TLS)
	}
	if u, err := url.Parse(c.Server.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("http.public_url must be an absolute http(s) URL, got %q", c.Server.PublicURL)
	}
	if !strings.Contains(c.Registration.VerificationURLTemplate, "{token}") {
		return fmt.Errorf("registration.verification_url_template must contain {token}")
	}
	if c.Registration.VerificationTTLHours <= 0 {
		return fmt.Errorf("registration.verification_ttl_hours must be positive")
	}
	return nil
}
