package application

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
)

// Secrets and variables rules:
//   - they belong to a scope, managed like its runners: the site (admins),
//     a group (its owner) or a repository (maintainers and up);
//   - secrets are sealed with security.encryption_key (unavailable without
//     it) and never read back through the API, only their names;
//     variables are plain and readable by those who manage them;
//   - names are letters, digits and _, not starting with a digit or
//     GITHUB_, case-insensitive (stored upper case); values up to 48 KB;
//   - a run gets the site's, then its group's (repositories in a group's
//     namespace), then its repository's, the more specific winning; runs
//     from forks get none.

var (
	ErrSecretsUnavailable = errors.New("secrets are unavailable: the server has no security.encryption_key")
	ErrInvalidSecretName  = errors.New("names are letters, digits and _, not starting with a digit or GITHUB_")
	ErrSecretTooLarge     = errors.New("values are at most 48 KB")
	ErrSecretNotFound     = errors.New("not found")
)

const maxSecretBytes = 48 << 10

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,99}$`)

func cleanSecretName(name string) (string, error) {
	name = strings.ToUpper(strings.TrimSpace(name))
	if !secretName.MatchString(name) || strings.HasPrefix(name, "GITHUB_") {
		return "", ErrInvalidSecretName
	}
	return name, nil
}

func scopeKey(s RunnerScope) (string, string) {
	if s.Id == nil {
		return s.Type, ""
	}
	return s.Type, *s.Id
}

// SecretInfo is what the API shows of a secret.
type SecretInfo struct {
	Name      string
	UpdatedAt time.Time
}

func (c *ActionsApp) ListSecrets(actorId string, scope RunnerScope) ([]SecretInfo, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return nil, err
	}
	t, id := scopeKey(scope)
	secrets, err := c.Pers.Secrets(t, id)
	if err != nil {
		return nil, err
	}
	out := make([]SecretInfo, 0, len(secrets))
	for _, s := range secrets {
		out = append(out, SecretInfo{Name: s.Name, UpdatedAt: s.UpdatedAt})
	}
	return out, nil
}

func (c *ActionsApp) PutSecret(actorId string, scope RunnerScope, name, value string) (string, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return "", err
	}
	if c.Box == nil {
		return "", ErrSecretsUnavailable
	}
	name, err := cleanSecretName(name)
	if err != nil {
		return "", err
	}
	if len(value) > maxSecretBytes {
		return "", ErrSecretTooLarge
	}
	sealed, err := c.Box.Seal(value)
	if err != nil {
		return "", err
	}
	t, id := scopeKey(scope)
	now := c.now()
	if err := c.Pers.PutSecret(&domain.ActionSecret{ScopeType: t, ScopeId: id, Name: name, Value: sealed, CreatedAt: now, UpdatedAt: now}); err != nil {
		return "", err
	}
	c.Logger.Info().Str("component", "audit").Str("scope", t).Str("scope_id", id).Str("secret", name).Str("by", actorId).Msg("actions secret set")
	return name, nil
}

func (c *ActionsApp) DeleteSecret(actorId string, scope RunnerScope, name string) error {
	if err := c.CanManage(actorId, scope); err != nil {
		return err
	}
	t, id := scopeKey(scope)
	ok, err := c.Pers.DeleteSecret(t, id, strings.ToUpper(strings.TrimSpace(name)))
	if err != nil {
		return err
	}
	if !ok {
		return ErrSecretNotFound
	}
	return nil
}

func (c *ActionsApp) ListVariables(actorId string, scope RunnerScope) ([]domain.ActionVariable, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return nil, err
	}
	t, id := scopeKey(scope)
	return c.Pers.Variables(t, id)
}

func (c *ActionsApp) PutVariable(actorId string, scope RunnerScope, name, value string) (string, error) {
	if err := c.CanManage(actorId, scope); err != nil {
		return "", err
	}
	name, err := cleanSecretName(name)
	if err != nil {
		return "", err
	}
	if len(value) > maxSecretBytes {
		return "", ErrSecretTooLarge
	}
	t, id := scopeKey(scope)
	now := c.now()
	return name, c.Pers.PutVariable(&domain.ActionVariable{ScopeType: t, ScopeId: id, Name: name, Value: value, CreatedAt: now, UpdatedAt: now})
}

func (c *ActionsApp) DeleteVariable(actorId string, scope RunnerScope, name string) error {
	if err := c.CanManage(actorId, scope); err != nil {
		return err
	}
	t, id := scopeKey(scope)
	ok, err := c.Pers.DeleteVariable(t, id, strings.ToUpper(strings.TrimSpace(name)))
	if err != nil {
		return err
	}
	if !ok {
		return ErrSecretNotFound
	}
	return nil
}

// runScopes are the scopes whose secrets a repository's runs get, least
// specific first.
func runScopes(repo *domain.Repository) [][2]string {
	scopes := [][2]string{{domain.RunnerScopeSite, ""}}
	if repo.OwnerType == domain.OwnerGroup {
		scopes = append(scopes, [2]string{domain.RunnerScopeGroup, repo.OwnerId})
	}
	return append(scopes, [2]string{domain.RunnerScopeRepository, repo.Id})
}

// runSecrets returns the secrets and variables of a run (none from forks).
func (c *ActionsApp) runSecrets(repo *domain.Repository, run *domain.ActionRun) (map[string]string, map[string]string, error) {
	secrets, vars := map[string]string{}, map[string]string{}
	if run.FromFork {
		return secrets, vars, nil
	}
	for _, s := range runScopes(repo) {
		vs, err := c.Pers.Variables(s[0], s[1])
		if err != nil {
			return nil, nil, err
		}
		for _, v := range vs {
			vars[v.Name] = v.Value
		}
		if c.Box == nil {
			continue
		}
		ss, err := c.Pers.Secrets(s[0], s[1])
		if err != nil {
			return nil, nil, err
		}
		for _, sec := range ss {
			plain, err := c.Box.Open(sec.Value)
			if err != nil {
				c.Logger.Error().Err(err).Str("component", "application.actions").Str("secret", sec.Name).Msg("failed to open a secret")
				continue
			}
			secrets[sec.Name] = plain
		}
	}
	return secrets, vars, nil
}
