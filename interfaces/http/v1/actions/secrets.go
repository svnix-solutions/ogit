package actions

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
)

type Secret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SecretsResponse struct {
	Secrets []Secret `json:"secrets"`
}

type Variable struct {
	Name      string    `json:"name"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

type VariablesResponse struct {
	Variables []Variable `json:"variables"`
}

type SiteNameRequest struct {
	Name  string `path:"name" json:"-" validate:"required"`
	Value string `json:"value"`
}

type GroupNameRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `path:"name" json:"-" validate:"required"`
	Value      string `json:"value"`
}

type RepoNameRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Name       string `path:"name" json:"-" validate:"required"`
	Value      string `json:"value"`
}

type NameResponse struct {
	Name string `json:"name"`
}

type scopeFunc = func(string) (application.RunnerScope, error)

func setupSecrets(c Controller, grp, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Actions"}
	opts := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	secretDesc := " Secrets are encrypted (security.encryption_key) and never read back. Names: letters, digits and _, not GITHUB_."
	for _, s := range []struct {
		kind, who string
	}{{"site", "Site admins."}, {"group", "The group owner."}, {"repo", "Maintainers and owners."}} {
		switch s.kind {
		case "site":
			fiberoapi.Get(grp, "/admin/actions/secrets", c.SiteSecrets, opts("List shared secrets", "actions.site.secrets", s.who+" Names only."))
			fiberoapi.Put(grp, "/admin/actions/secrets/:name", c.SitePutSecret, opts("Set a shared secret", "actions.site.secret.put", s.who+secretDesc))
			fiberoapi.Delete(grp, "/admin/actions/secrets/:name", c.SiteDeleteSecret, opts("Delete a shared secret", "actions.site.secret.delete", s.who))
			fiberoapi.Get(grp, "/admin/actions/variables", c.SiteVariables, opts("List shared variables", "actions.site.variables", s.who))
			fiberoapi.Put(grp, "/admin/actions/variables/:name", c.SitePutVariable, opts("Set a shared variable", "actions.site.variable.put", s.who))
			fiberoapi.Delete(grp, "/admin/actions/variables/:name", c.SiteDeleteVariable, opts("Delete a shared variable", "actions.site.variable.delete", s.who))
		case "group":
			fiberoapi.Get(grp, "/group/:identifier/actions/secrets", c.GroupSecrets, opts("List group secrets", "actions.group.secrets", s.who+" Names only."))
			fiberoapi.Put(grp, "/group/:identifier/actions/secrets/:name", c.GroupPutSecret, opts("Set a group secret", "actions.group.secret.put", s.who+secretDesc))
			fiberoapi.Delete(grp, "/group/:identifier/actions/secrets/:name", c.GroupDeleteSecret, opts("Delete a group secret", "actions.group.secret.delete", s.who))
			fiberoapi.Get(grp, "/group/:identifier/actions/variables", c.GroupVariables, opts("List group variables", "actions.group.variables", s.who))
			fiberoapi.Put(grp, "/group/:identifier/actions/variables/:name", c.GroupPutVariable, opts("Set a group variable", "actions.group.variable.put", s.who))
			fiberoapi.Delete(grp, "/group/:identifier/actions/variables/:name", c.GroupDeleteVariable, opts("Delete a group variable", "actions.group.variable.delete", s.who))
		case "repo":
			fiberoapi.Get(repoGrp, "/:identifier/actions/secrets", c.RepoSecrets, opts("List repository secrets", "actions.repo.secrets", s.who+" Names only."))
			fiberoapi.Put(repoGrp, "/:identifier/actions/secrets/:name", c.RepoPutSecret, opts("Set a repository secret", "actions.repo.secret.put", s.who+secretDesc))
			fiberoapi.Delete(repoGrp, "/:identifier/actions/secrets/:name", c.RepoDeleteSecret, opts("Delete a repository secret", "actions.repo.secret.delete", s.who))
			fiberoapi.Get(repoGrp, "/:identifier/actions/variables", c.RepoVariables, opts("List repository variables", "actions.repo.variables", s.who))
			fiberoapi.Put(repoGrp, "/:identifier/actions/variables/:name", c.RepoPutVariable, opts("Set a repository variable", "actions.repo.variable.put", s.who))
			fiberoapi.Delete(repoGrp, "/:identifier/actions/variables/:name", c.RepoDeleteVariable, opts("Delete a repository variable", "actions.repo.variable.delete", s.who))
		}
	}
}

func (c Controller) withScope(ctx *fiber.Ctx, scope scopeFunc) (string, application.RunnerScope, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return "", application.RunnerScope{}, e
	}
	s, err := scope(user)
	if err != nil {
		return "", application.RunnerScope{}, apierror.From(err, c.Logger)
	}
	return user, s, nil
}

func (c Controller) listSecrets(ctx *fiber.Ctx, scope scopeFunc) (*SecretsResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	secrets, err := c.Actions.ListSecrets(user, s)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &SecretsResponse{Secrets: make([]Secret, 0, len(secrets))}
	for _, sec := range secrets {
		resp.Secrets = append(resp.Secrets, Secret{Name: sec.Name, UpdatedAt: sec.UpdatedAt})
	}
	return resp, nil
}

func (c Controller) putSecret(ctx *fiber.Ctx, scope scopeFunc, name, value string) (*NameResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	name, err := c.Actions.PutSecret(user, s, name, value)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &NameResponse{Name: name}, nil
}

func (c Controller) deleteSecret(ctx *fiber.Ctx, scope scopeFunc, name string) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	if err := c.Actions.DeleteSecret(user, s, name); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "secret deleted"}, nil
}

func (c Controller) listVariables(ctx *fiber.Ctx, scope scopeFunc) (*VariablesResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	vars, err := c.Actions.ListVariables(user, s)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &VariablesResponse{Variables: make([]Variable, 0, len(vars))}
	for _, v := range vars {
		resp.Variables = append(resp.Variables, Variable{Name: v.Name, Value: v.Value, UpdatedAt: v.UpdatedAt})
	}
	return resp, nil
}

func (c Controller) putVariable(ctx *fiber.Ctx, scope scopeFunc, name, value string) (*NameResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	name, err := c.Actions.PutVariable(user, s, name, value)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &NameResponse{Name: name}, nil
}

func (c Controller) deleteVariable(ctx *fiber.Ctx, scope scopeFunc, name string) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, s, e := c.withScope(ctx, scope)
	if e != nil {
		return nil, e
	}
	if err := c.Actions.DeleteVariable(user, s, name); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "variable deleted"}, nil
}

func (c Controller) SiteSecrets(ctx *fiber.Ctx, _ SiteRequest) (*SecretsResponse, *fiberoapi.ErrorResponse) {
	return c.listSecrets(ctx, site)
}
func (c Controller) SitePutSecret(ctx *fiber.Ctx, req SiteNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putSecret(ctx, site, req.Name, req.Value)
}
func (c Controller) SiteDeleteSecret(ctx *fiber.Ctx, req SiteNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteSecret(ctx, site, req.Name)
}
func (c Controller) SiteVariables(ctx *fiber.Ctx, _ SiteRequest) (*VariablesResponse, *fiberoapi.ErrorResponse) {
	return c.listVariables(ctx, site)
}
func (c Controller) SitePutVariable(ctx *fiber.Ctx, req SiteNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putVariable(ctx, site, req.Name, req.Value)
}
func (c Controller) SiteDeleteVariable(ctx *fiber.Ctx, req SiteNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteVariable(ctx, site, req.Name)
}

func (c Controller) GroupSecrets(ctx *fiber.Ctx, req GroupRequest) (*SecretsResponse, *fiberoapi.ErrorResponse) {
	return c.listSecrets(ctx, c.group(req.Identifier))
}
func (c Controller) GroupPutSecret(ctx *fiber.Ctx, req GroupNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putSecret(ctx, c.group(req.Identifier), req.Name, req.Value)
}
func (c Controller) GroupDeleteSecret(ctx *fiber.Ctx, req GroupNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteSecret(ctx, c.group(req.Identifier), req.Name)
}
func (c Controller) GroupVariables(ctx *fiber.Ctx, req GroupRequest) (*VariablesResponse, *fiberoapi.ErrorResponse) {
	return c.listVariables(ctx, c.group(req.Identifier))
}
func (c Controller) GroupPutVariable(ctx *fiber.Ctx, req GroupNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putVariable(ctx, c.group(req.Identifier), req.Name, req.Value)
}
func (c Controller) GroupDeleteVariable(ctx *fiber.Ctx, req GroupNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteVariable(ctx, c.group(req.Identifier), req.Name)
}

func (c Controller) RepoSecrets(ctx *fiber.Ctx, req RepoRequest) (*SecretsResponse, *fiberoapi.ErrorResponse) {
	return c.listSecrets(ctx, c.repo(req.Identifier))
}
func (c Controller) RepoPutSecret(ctx *fiber.Ctx, req RepoNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putSecret(ctx, c.repo(req.Identifier), req.Name, req.Value)
}
func (c Controller) RepoDeleteSecret(ctx *fiber.Ctx, req RepoNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteSecret(ctx, c.repo(req.Identifier), req.Name)
}
func (c Controller) RepoVariables(ctx *fiber.Ctx, req RepoRequest) (*VariablesResponse, *fiberoapi.ErrorResponse) {
	return c.listVariables(ctx, c.repo(req.Identifier))
}
func (c Controller) RepoPutVariable(ctx *fiber.Ctx, req RepoNameRequest) (*NameResponse, *fiberoapi.ErrorResponse) {
	return c.putVariable(ctx, c.repo(req.Identifier), req.Name, req.Value)
}
func (c Controller) RepoDeleteVariable(ctx *fiber.Ctx, req RepoNameRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.deleteVariable(ctx, c.repo(req.Identifier), req.Name)
}
