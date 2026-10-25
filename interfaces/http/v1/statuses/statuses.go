// Package statuses serves commit statuses under /api/v1/repository/:identifier.
// They accept a session or a personal access token (CI), so the routes are
// Security "disabled" and authenticate with optauth.Caller.
package statuses

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp
	Limits     *ratelimit.Guard
	Statuses   *application.StatusApp
}

type Status struct {
	Id          string    `json:"id"`
	State       string    `json:"state"`
	Context     string    `json:"context"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	Creator     string    `json:"creator,omitempty"` // username; empty once deleted
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateRequest struct {
	Identifier  string `path:"identifier" json:"-" validate:"required"`
	SHA         string `path:"sha" json:"-" validate:"required"`
	State       string `json:"state" validate:"required"`
	Context     string `json:"context"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
}

type ListRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	SHA        string `path:"sha" json:"-" validate:"required"` // or a branch or tag
}

type ListResponse struct {
	SHA      string   `json:"sha"`
	Statuses []Status `json:"statuses"`
}

type CombinedRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Ref        string `query:"ref" json:"-"` // branch, tag or commit; default HEAD
}

type CombinedResponse struct {
	SHA        string   `json:"sha"`
	State      string   `json:"state"` // failure, pending (also when none) or success
	TotalCount int      `json:"total_count"`
	Statuses   []Status `json:"statuses"` // latest per context
}

type BatchRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	SHAs       string `query:"shas" json:"-"` // comma-separated full hashes, up to 100
}

type BatchResponse struct {
	// Commits maps each hash with statuses to its combined status.
	Commits map[string]CombinedResponse `json:"commits"`
}

type ContextsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ContextsResponse struct {
	Contexts []string `json:"contexts"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Commit statuses"}
	opts := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled",
			Description: desc + " Authenticate with a session or a personal access token (Authorization: token <pat>)."}
	}
	fiberoapi.Post(repoGrp, "/:identifier/statuses/:sha", c.Create, opts("Post a commit status", "statuses.create",
		"Developers and up; tokens need write:status or write:repository. state: pending, success, failure or error; context (default \"default\"), description (140 characters), target_url (http or https)."))
	fiberoapi.Get(repoGrp, "/:identifier/statuses/:sha", c.List, opts("List a commit's statuses", "statuses.list", "Newest first, at most 100. sha may also be a branch or tag."))
	fiberoapi.Get(repoGrp, "/:identifier/status", c.Combined, opts("Combined status", "statuses.combined", "The latest status per context of ?ref= (default HEAD) and their combined state."))
	fiberoapi.Get(repoGrp, "/:identifier/statuses", c.Batch, opts("Combined statuses of several commits", "statuses.batch", "?shas=<hash>,<hash> (up to 100); commits without statuses are left out."))
	fiberoapi.Get(repoGrp, "/:identifier/status-contexts", c.Contexts, opts("Recent status contexts", "statuses.contexts", "Contexts reported in the last 30 days, most recent first."))
}

func ToStatus(s domain.CommitStatus) Status {
	out := Status{Id: s.Id, State: string(s.State), Context: s.Context, Description: s.Description, TargetURL: s.TargetURL, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
	if s.Creator != nil {
		out.Creator = s.Creator.Username
	}
	return out
}

func statuses(list []domain.CommitStatus) []Status {
	out := make([]Status, 0, len(list))
	for _, s := range list {
		out = append(out, ToStatus(s))
	}
	return out
}

func combined(c application.Combined) CombinedResponse {
	return CombinedResponse{SHA: c.SHA, State: string(c.State), TotalCount: len(c.Statuses), Statuses: statuses(c.Statuses)}
}

func (c Controller) caller(ctx *fiber.Ctx) (application.StatusCaller, *fiberoapi.ErrorResponse) {
	caller, err := optauth.Caller(ctx, c.SessionApp, c.TokenApp, c.Limits)
	var many *optauth.ErrTooManyFailures
	switch {
	case errors.As(err, &many):
		return caller, &fiberoapi.ErrorResponse{Code: fiber.StatusTooManyRequests, Details: err.Error(), Type: "RATE_LIMITED"}
	case err != nil:
		return caller, apierror.From(err, c.Logger)
	}
	return caller, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Status, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	if caller.UserId == "" {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	s, err := c.Statuses.Create(id, path, caller, req.SHA, application.StatusInput{State: req.State, Context: req.Context, Description: req.Description, TargetURL: req.TargetURL})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := ToStatus(*s)
	return &out, nil
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	_, sha, list, err := c.Statuses.List(id, path, caller, req.SHA)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ListResponse{SHA: sha, Statuses: statuses(list)}, nil
}

func (c Controller) Combined(ctx *fiber.Ctx, req CombinedRequest) (*CombinedResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	_, comb, err := c.Statuses.CombinedFor(id, path, caller, req.Ref)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := combined(*comb)
	return &out, nil
}

func (c Controller) Batch(ctx *fiber.Ctx, req BatchRequest) (*BatchResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	var shas []string
	for _, s := range strings.Split(req.SHAs, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			shas = append(shas, s)
		}
	}
	id, path := repoid.Split(req.Identifier)
	all, err := c.Statuses.Batch(id, path, caller, shas)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &BatchResponse{Commits: map[string]CombinedResponse{}}
	for sha, comb := range all {
		resp.Commits[sha] = combined(comb)
	}
	return resp, nil
}

func (c Controller) Contexts(ctx *fiber.Ctx, req ContextsRequest) (*ContextsResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	contexts, err := c.Statuses.RecentContexts(id, path, caller)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ContextsResponse{Contexts: contexts}, nil
}
