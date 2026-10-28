package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEmailVerification(t *testing.T) {
	valid := func() Config {
		var c Config
		c.Registration.RequireEmailVerification = true
		c.Registration.VerificationTTLHours = 24
		c.Registration.VerificationURLTemplate = DefaultVerificationURLTemplate
		c.SMTP.Host = "smtp.example.com"
		c.SMTP.From = "oGit <git@example.com>"
		c.SMTP.TLS = "starttls"
		c.Server.PublicURL = "https://git.example.com"
		return c
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid"},
		{name: "disabled_needs_nothing", mutate: func(c *Config) { *c = Config{} }},
		{name: "missing_settings_listed", mutate: func(c *Config) { c.SMTP.Host, c.SMTP.From, c.Server.PublicURL = "", "", "" }, wantErr: "smtp.host, smtp.from, http.public_url"},
		{name: "bad_from", mutate: func(c *Config) { c.SMTP.From = "nope" }, wantErr: "smtp.from"},
		{name: "bad_tls", mutate: func(c *Config) { c.SMTP.TLS = "ssl" }, wantErr: "smtp.tls"},
		{name: "relative_public_url", mutate: func(c *Config) { c.Server.PublicURL = "git.example.com" }, wantErr: "http.public_url"},
		{name: "template_without_token", mutate: func(c *Config) { c.Registration.VerificationURLTemplate = "{public_url}/verify" }, wantErr: "{token}"},
		{name: "zero_ttl", mutate: func(c *Config) { c.Registration.VerificationTTLHours = 0 }, wantErr: "verification_ttl_hours"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			if tt.mutate != nil {
				tt.mutate(&c)
			}
			err := c.ValidateEmailVerification()
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}
