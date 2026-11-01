// Package workspaceagent serves the protocol of `ogit workspace-agent` under
// /api/workspaces/agent: registration, and a long-polled sync in which the
// agent reports its containers and gets what to do. Agents authenticate with
// "Authorization: Bearer ogita_...".
package workspaceagent

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/rs/zerolog"
)

// maxWait bounds a sync's long poll.
const maxWait = 30 * time.Second

type Controller struct {
	Logger     zerolog.Logger
	Workspaces *application.WorkspaceApp
	Limits     *ratelimit.Guard
	// PollInterval: how often a waiting sync looks for work (1s if 0).
	PollInterval time.Duration
}

func Setup(c Controller, app *fiber.App) {
	const base = "/api/workspaces/agent"
	app.Post(base+"/register", c.Register)
	app.Post(base+"/sync", c.Sync)
}

func fail(ctx *fiber.Ctx, status int, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"message": msg})
}

type registerRequest struct {
	Token   string `json:"token"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (c Controller) blocked(ctx *fiber.Ctx) bool {
	r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), c.Limits.ClientIP(ctx), "")
	if blocked {
		ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
	}
	return blocked
}

func (c Controller) Register(ctx *fiber.Ctx) error {
	var req registerRequest
	if err := ctx.BodyParser(&req); err != nil {
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	if c.blocked(ctx) {
		return fail(ctx, fiber.StatusTooManyRequests, "too many failed attempts")
	}
	a, token, err := c.Workspaces.RegisterAgent(req.Token, req.Name, req.Version)
	if errors.Is(err, application.ErrAgentRegistration) {
		c.Limits.LoginFailed(ctx.UserContext(), c.Limits.ClientIP(ctx), "")
		return fail(ctx, fiber.StatusUnauthorized, err.Error())
	} else if err != nil {
		c.Logger.Error().Err(err).Str("component", "http.workspaceagent").Msg("agent registration failed")
		return fail(ctx, fiber.StatusInternalServerError, "internal error")
	}
	return ctx.JSON(fiber.Map{"id": a.Id, "name": a.Name, "token": token})
}

// Sync records the report, then answers tasks, waiting for some up to
// report.wait seconds.
func (c Controller) Sync(ctx *fiber.Ctx) error {
	if c.blocked(ctx) {
		return fail(ctx, fiber.StatusTooManyRequests, "too many failed authentication attempts")
	}
	token, _ := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
	a, err := c.Workspaces.AuthenticateAgent(token)
	if err != nil {
		c.Limits.LoginFailed(ctx.UserContext(), c.Limits.ClientIP(ctx), "")
		return fail(ctx, fiber.StatusUnauthorized, "invalid agent token")
	}
	var report application.AgentReport
	if err := ctx.BodyParser(&report); err != nil {
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	if err := c.Workspaces.Report(a, report); err != nil {
		c.Logger.Error().Err(err).Str("component", "http.workspaceagent").Msg("agent report failed")
		return fail(ctx, fiber.StatusInternalServerError, "internal error")
	}
	interval := c.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(min(time.Duration(max(report.Wait, 0))*time.Second, maxWait))
	for {
		tasks, err := c.Workspaces.Tasks(a, report)
		if err != nil {
			c.Logger.Error().Err(err).Str("component", "http.workspaceagent").Msg("agent tasks failed")
			return fail(ctx, fiber.StatusInternalServerError, "internal error")
		}
		if !tasks.Empty() || time.Now().Add(interval).After(deadline) {
			return ctx.JSON(tasks)
		}
		time.Sleep(interval)
	}
}
