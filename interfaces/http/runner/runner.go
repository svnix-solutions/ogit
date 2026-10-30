// Package runner serves the protocol of `ogit runner` under
// /api/actions/runner: registration, long-polled task fetching, state and
// log reports. Runners authenticate with "Authorization: Bearer ogitr_...".
package runner

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/rs/zerolog"
)

// maxWait bounds a fetch's long poll.
const maxWait = 30 * time.Second

type Controller struct {
	Config  config.Config
	Logger  zerolog.Logger
	Actions *application.ActionsApp
	Limits  *ratelimit.Guard
	// PollInterval: how often a waiting fetch looks for work (1s if 0).
	PollInterval time.Duration
}

func Setup(c Controller, app *fiber.App) {
	const base = "/api/actions/runner"
	app.Post(base+"/register", c.Register)
	app.Post(base+"/fetch", c.authorized(c.Fetch))
	app.Post(base+"/runs/:run_id/state", c.authorized(c.State))
	app.Post(base+"/runs/:run_id/log", c.authorized(c.Log))
}

func fail(ctx *fiber.Ctx, status int, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"message": msg})
}

const runnerLocal = "actions_runner"

func (c Controller) authorized(h fiber.Handler) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		ip := c.Limits.ClientIP(ctx)
		if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
			ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
			return fail(ctx, fiber.StatusTooManyRequests, "too many failed authentication attempts")
		}
		token, _ := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
		r, err := c.Actions.AuthenticateRunner(token)
		if err != nil {
			c.Limits.LoginFailed(ctx.UserContext(), ip, "")
			return fail(ctx, fiber.StatusUnauthorized, "invalid runner token")
		}
		ctx.Locals(runnerLocal, r)
		return h(ctx)
	}
}

func runnerOf(ctx *fiber.Ctx) *domain.ActionRunner {
	r, _ := ctx.Locals(runnerLocal).(*domain.ActionRunner)
	return r
}

type registerRequest struct {
	Token   string   `json:"token"`
	Name    string   `json:"name"`
	Labels  []string `json:"labels"`
	Version string   `json:"version"`
	// Ephemeral: the runner takes one run, and is deleted when it ends.
	Ephemeral bool `json:"ephemeral"`
}

func (c Controller) Register(ctx *fiber.Ctx) error {
	var req registerRequest
	if err := ctx.BodyParser(&req); err != nil {
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	ip := c.Limits.ClientIP(ctx)
	if r, blocked := c.Limits.LoginBlocked(ctx.UserContext(), ip, ""); blocked {
		ctx.Set(fiber.HeaderRetryAfter, ratelimit.RetryAfter(r))
		return fail(ctx, fiber.StatusTooManyRequests, "too many failed attempts")
	}
	r, token, err := c.Actions.Register(req.Token, req.Name, req.Labels, req.Version, req.Ephemeral)
	if errors.Is(err, application.ErrRegistrationToken) {
		c.Limits.LoginFailed(ctx.UserContext(), ip, "")
		return fail(ctx, fiber.StatusUnauthorized, err.Error())
	} else if err != nil {
		c.Logger.Error().Err(err).Str("component", "http.runner").Msg("runner registration failed")
		return fail(ctx, fiber.StatusInternalServerError, "internal error")
	}
	return ctx.JSON(fiber.Map{"id": r.Id, "name": r.Name, "token": token, "scope": r.ScopeType, "ephemeral": r.Ephemeral})
}

type fetchRequest struct {
	Labels  []string `json:"labels"`
	Version string   `json:"version"`
	// Wait: seconds to wait for work (at most 30).
	Wait int `json:"wait"`
}

func (c Controller) serverURL(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u
	}
	return ctx.BaseURL()
}

// Fetch answers a task (200) or, after waiting, nothing (204).
func (c Controller) Fetch(ctx *fiber.Ctx) error {
	var req fetchRequest
	if len(ctx.Body()) > 0 {
		if err := ctx.BodyParser(&req); err != nil {
			return fail(ctx, fiber.StatusBadRequest, "invalid request")
		}
	}
	interval := c.PollInterval
	if interval <= 0 {
		interval = time.Second
	}
	wait := min(time.Duration(max(req.Wait, 0))*time.Second, maxWait)
	deadline := time.Now().Add(wait)
	r, url := runnerOf(ctx), c.serverURL(ctx)
	for {
		task, err := c.Actions.FetchTask(r, req.Labels, req.Version, url)
		if err != nil {
			c.Logger.Error().Err(err).Str("component", "http.runner").Msg("fetching a task failed")
			return fail(ctx, fiber.StatusInternalServerError, "internal error")
		}
		if task != nil {
			return ctx.JSON(task)
		}
		if time.Now().Add(interval).After(deadline) {
			return ctx.SendStatus(fiber.StatusNoContent)
		}
		select {
		case <-ctx.Context().Done():
			return nil
		case <-sleep(ctx.UserContext(), interval):
		}
	}
}

func sleep(ctx context.Context, d time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		}
		close(done)
	}()
	return done
}

func (c Controller) State(ctx *fiber.Ctx) error {
	var u application.StateUpdate
	if err := ctx.BodyParser(&u); err != nil {
		return fail(ctx, fiber.StatusBadRequest, "invalid request")
	}
	cancel, err := c.Actions.UpdateState(runnerOf(ctx), ctx.Params("run_id"), u)
	switch {
	case errors.Is(err, application.ErrRunNotRunning):
		return fail(ctx, fiber.StatusConflict, err.Error())
	case err != nil && strings.HasPrefix(err.Error(), "status must"):
		return fail(ctx, fiber.StatusBadRequest, err.Error())
	case err != nil:
		c.Logger.Error().Err(err).Str("component", "http.runner").Msg("state update failed")
		return fail(ctx, fiber.StatusInternalServerError, "internal error")
	}
	return ctx.JSON(fiber.Map{"cancel": cancel})
}

func (c Controller) Log(ctx *fiber.Ctx) error {
	err := c.Actions.AppendLog(runnerOf(ctx), ctx.Params("run_id"), ctx.Body())
	switch {
	case errors.Is(err, application.ErrRunNotRunning):
		return fail(ctx, fiber.StatusConflict, err.Error())
	case err != nil:
		c.Logger.Error().Err(err).Str("component", "http.runner").Msg("log upload failed")
		return fail(ctx, fiber.StatusInternalServerError, "internal error")
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}
