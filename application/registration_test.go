package application

import (
	"testing"

	"github.com/labbs/ogit/infrastructure/config"
	"github.com/stretchr/testify/assert"
)

func TestCheckRegistrationPolicy(t *testing.T) {
	base := func() config.Config {
		var cfg config.Config
		cfg.Registration.Enabled = true
		cfg.Registration.PasswordMinLength = 12
		cfg.Registration.PasswordComplexity = true
		return cfg
	}
	const good = "Correct-Horse-9"

	tests := []struct {
		name     string
		mutate   func(*config.Config)
		email    string
		password string
		want     error
	}{
		{name: "valid", email: "a@example.com", password: good},
		{name: "disabled", mutate: func(c *config.Config) { c.Registration.Enabled = false }, email: "a@example.com", password: good, want: ErrRegistrationDisabled},
		{name: "too_short", email: "a@example.com", password: "Sh0rt!", want: ErrRegistrationRejected},
		{name: "min_length_counts_runes", mutate: func(c *config.Config) { c.Registration.PasswordComplexity = false }, email: "a@example.com", password: "ééééééééééé", want: ErrRegistrationRejected},
		{name: "no_upper", email: "a@example.com", password: "correct-horse-9", want: ErrRegistrationRejected},
		{name: "no_lower", email: "a@example.com", password: "CORRECT-HORSE-9", want: ErrRegistrationRejected},
		{name: "no_digit", email: "a@example.com", password: "Correct-Horse-X", want: ErrRegistrationRejected},
		{name: "no_symbol", email: "a@example.com", password: "CorrectHorse99", want: ErrRegistrationRejected},
		{name: "complexity_off", mutate: func(c *config.Config) { c.Registration.PasswordComplexity = false }, email: "a@example.com", password: "correcthorsebattery"},
		{name: "whitelisted_domain", mutate: func(c *config.Config) { c.Registration.DomainWhitelist = []string{"example.com"} }, email: "a@Example.COM", password: good},
		{name: "whitelist_entry_with_at", mutate: func(c *config.Config) { c.Registration.DomainWhitelist = []string{"@example.com"} }, email: "a@example.com", password: good},
		{name: "domain_not_whitelisted", mutate: func(c *config.Config) { c.Registration.DomainWhitelist = []string{"example.com"} }, email: "a@evil.com", password: good, want: ErrRegistrationRejected},
		{name: "subdomain_not_whitelisted", mutate: func(c *config.Config) { c.Registration.DomainWhitelist = []string{"example.com"} }, email: "a@sub.example.com", password: good, want: ErrRegistrationRejected},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := checkRegistrationPolicy(cfg, tt.email, tt.password)
			if tt.want == nil {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, tt.want)
			}
		})
	}
}
