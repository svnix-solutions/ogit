package config

import (
	"errors"
	"github.com/labbs/ogit/domain"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// SSOProviderConfig is a sign-in provider declared in the configuration
// file (sso.providers), read-only in the admin pages. client_secret may
// name environment variables (${OIDC_SECRET}).
type SSOProviderConfig struct {
	Slug             string `yaml:"slug"`
	Name             string `yaml:"name"`
	Kind             string `yaml:"kind"` // oidc or github
	Issuer           string `yaml:"issuer"`
	ClientId         string `yaml:"client_id"`
	ClientSecret     string `yaml:"client_secret"`
	Scopes           string `yaml:"scopes"`
	AllowedDomains   string `yaml:"allowed_domains"`
	AllowedOrgs      string `yaml:"allowed_orgs"`
	AutoCreate       *bool  `yaml:"auto_create"` // default true
	RequireTwoFactor bool   `yaml:"require_two_factor"`
	Enabled          *bool  `yaml:"enabled"` // default true
	GroupsClaim      string `yaml:"groups_claim"`
	AdminGroup       string `yaml:"admin_group"`
	GroupMappings    []struct {
		Group  string `yaml:"group"`
		Target string `yaml:"target"`
		Role   string `yaml:"role"`
	} `yaml:"group_mappings"`
	// LDAP (kind ldap): issuer is the URL, client_id and client_secret the
	// service account.
	LDAP *domain.LDAPSettings `yaml:"ldap"`
	// SAML (kind saml): issuer is the IdP's metadata URL; client_secret the
	// SP's private key (PEM) and saml.certificate its certificate.
	SAML *domain.SAMLSettings `yaml:"saml"`
}

// LoadSSOProviders reads sso.providers from the configuration file (none
// when it doesn't exist, like the other settings).
func LoadSSOProviders(path string) ([]SSOProviderConfig, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var file struct {
		SSO struct {
			Providers []SSOProviderConfig `yaml:"providers"`
		} `yaml:"sso"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return nil, err
	}
	for i := range file.SSO.Providers {
		file.SSO.Providers[i].ClientSecret = os.ExpandEnv(file.SSO.Providers[i].ClientSecret)
	}
	return file.SSO.Providers, nil
}
