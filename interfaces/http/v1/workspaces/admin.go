package workspaces

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
)

// AdminWorkspace is a workspace seen by admins.
type AdminWorkspace struct {
	Workspace
	UserId  string `json:"user_id"`
	AgentId string `json:"agent_id,omitempty"`
}

type AdminListResponse struct {
	Workspaces []AdminWorkspace `json:"workspaces"`
}

type AdminListRequest struct{}

type Agent struct {
	Id         string    `json:"id"`
	Name       string    `json:"name"`
	Version    string    `json:"version"`
	Online     bool      `json:"online"`
	LastSeenAt time.Time `json:"last_seen_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type AgentsResponse struct {
	Agents []Agent `json:"agents"`
}

type AgentRequest struct {
	AgentId string `path:"agent_id" json:"-" validate:"required"`
}

type AgentTokenRequest struct {
	Reusable  bool       `json:"reusable"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expires_at"` // reusable tokens only
}

// AgentToken is a reusable registration token. The token itself is only
// shown when it's created.
type AgentToken struct {
	Id         string     `json:"id"`
	Name       string     `json:"name"`
	Reusable   bool       `json:"reusable"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Agents     int64      `json:"agents"`
}

type AgentTokenResponse struct {
	AgentToken
	Token string `json:"token"`
}

type AgentTokensResponse struct {
	Tokens []AgentToken `json:"tokens"`
}

type TokenIdRequest struct {
	TokenId string `path:"token_id" json:"-" validate:"required"`
}

func setupAdmin(c Controller, adminGrp *fiberoapi.OApiGroup, options func(summary, id, desc string) fiberoapi.OpenAPIOptions) {
	fiberoapi.Get(adminGrp, "/workspaces", c.AdminList, options("List all workspaces", "workspaces.admin.list", "Admins."))
	fiberoapi.Post(adminGrp, "/workspaces/:workspace_id/stop", c.AdminStop, options("Stop anyone's workspace", "workspaces.admin.stop", "Admins; logged."))
	fiberoapi.Delete(adminGrp, "/workspaces/:workspace_id", c.AdminDelete, options("Delete anyone's workspace", "workspaces.admin.delete", "Admins; logged. Running ones too, with their files."))
	fiberoapi.Get(adminGrp, "/workspace-agents", c.Agents, options("List workspace agents", "workspaces.agents", "Admins."))
	fiberoapi.Delete(adminGrp, "/workspace-agents/:agent_id", c.DeleteAgent, options("Delete a workspace agent", "workspaces.agents.delete", "Admins. Its workspaces stop and start afresh elsewhere; remove leftover containers on the host."))
	fiberoapi.Post(adminGrp, "/workspace-agents/registration-token", c.CreateAgentToken, options("Create an agent registration token", "workspaces.agents.token", "Admins. One-time for an hour, or {reusable, name, expires_at}."))
	fiberoapi.Get(adminGrp, "/workspace-agents/registration-tokens", c.AgentTokens, options("List reusable agent registration tokens", "workspaces.agents.tokens", "Admins."))
	fiberoapi.Delete(adminGrp, "/workspace-agents/registration-tokens/:token_id", c.RevokeAgentToken, options("Revoke an agent registration token", "workspaces.agents.tokens.revoke", "Admins. The agents it registered stay."))
}

func (c Controller) AdminList(ctx *fiber.Ctx, _ AdminListRequest) (*AdminListResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	views, err := c.Workspaces.AdminList(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &AdminListResponse{Workspaces: make([]AdminWorkspace, 0, len(views))}
	for _, v := range views {
		w := AdminWorkspace{Workspace: toWorkspace(v), UserId: v.UserId}
		if v.AgentId != nil {
			w.AgentId = *v.AgentId
		}
		resp.Workspaces = append(resp.Workspaces, w)
	}
	return resp, nil
}

func (c Controller) AdminStop(ctx *fiber.Ctx, req IdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.admin(ctx, "workspace stopping", func(user string) error { return c.Workspaces.AdminStop(user, req.WorkspaceId) })
}

func (c Controller) AdminDelete(ctx *fiber.Ctx, req IdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.admin(ctx, "workspace deleted", func(user string) error { return c.Workspaces.AdminDelete(user, req.WorkspaceId) })
}

func (c Controller) DeleteAgent(ctx *fiber.Ctx, req AgentRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.admin(ctx, "agent deleted", func(user string) error { return c.Workspaces.DeleteAgent(user, req.AgentId) })
}

func (c Controller) RevokeAgentToken(ctx *fiber.Ctx, req TokenIdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	return c.admin(ctx, "token revoked", func(user string) error { return c.Workspaces.RevokeAgentToken(user, req.TokenId) })
}

func (c Controller) admin(ctx *fiber.Ctx, message string, do func(user string) error) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	if err := do(user); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: message}, nil
}

func (c Controller) Agents(ctx *fiber.Ctx, _ AdminListRequest) (*AgentsResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	agents, err := c.Workspaces.Agents(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &AgentsResponse{Agents: make([]Agent, 0, len(agents))}
	for _, a := range agents {
		resp.Agents = append(resp.Agents, Agent{Id: a.Id, Name: a.Name, Version: a.Version, Online: application.AgentOnline(a), LastSeenAt: a.LastSeenAt, CreatedAt: a.CreatedAt})
	}
	return resp, nil
}

func (c Controller) CreateAgentToken(ctx *fiber.Ctx, req AgentTokenRequest) (*AgentTokenResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	t, raw, err := c.Workspaces.AgentToken(user, application.AgentTokenOptions{Reusable: req.Reusable, Name: req.Name, ExpiresAt: req.ExpiresAt})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &AgentTokenResponse{Token: raw, AgentToken: AgentToken{Id: t.Id, Name: t.Name, Reusable: t.Reusable, ExpiresAt: t.ExpiresAt, CreatedAt: t.CreatedAt}}, nil
}

func (c Controller) AgentTokens(ctx *fiber.Ctx, _ AdminListRequest) (*AgentTokensResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	tokens, counts, err := c.Workspaces.AgentTokens(user)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &AgentTokensResponse{Tokens: make([]AgentToken, 0, len(tokens))}
	for _, t := range tokens {
		resp.Tokens = append(resp.Tokens, AgentToken{Id: t.Id, Name: t.Name, Reusable: t.Reusable, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt, CreatedAt: t.CreatedAt, Agents: counts[t.Id]})
	}
	return resp, nil
}
