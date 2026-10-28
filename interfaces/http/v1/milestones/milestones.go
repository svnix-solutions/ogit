// Package milestones serves /api/v1/repository/:identifier/milestones.
package milestones

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	TokenApp   *application.TokenApp // repository tokens read the repository's routes
	Milestones *application.MilestoneApp
}

type Milestone struct {
	Id          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	DueOn       *time.Time `json:"due_on"`
	State       string     `json:"state"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	Open        int64      `json:"open"`   // open issues and pull requests
	Closed      int64      `json:"closed"` // closed or merged
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type ListRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	State      string `query:"state" json:"-"` // open (default), closed or all
}

type ListResponse struct {
	Milestones []Milestone `json:"milestones"`
}

type Fields struct {
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	DueOn       *time.Time `json:"due_on"`
	ClearDueOn  bool       `json:"clear_due_on"` // remove the due date
	State       *string    `json:"state"`        // open or closed
}

type CreateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Fields
}

type IdRequest struct {
	Identifier  string `path:"identifier" json:"-" validate:"required"`
	MilestoneId string `path:"milestone_id" json:"-" validate:"required"`
}

type UpdateRequest struct {
	Identifier  string `path:"identifier" json:"-" validate:"required"`
	MilestoneId string `path:"milestone_id" json:"-" validate:"required"`
	Fields
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Milestones"}
	public := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled", Description: desc + " No session needed for public repositories."}
	}
	private := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	fiberoapi.Get(repoGrp, "/:identifier/milestones", c.List, public("List milestones", "milestones.list", "?state=open (default), closed or all; soonest due first, with progress (issues and pull requests)."))
	fiberoapi.Post(repoGrp, "/:identifier/milestones", c.Create, private("Create a milestone", "milestones.create", "Maintainers and owners. title, description, due_on."))
	fiberoapi.Get(repoGrp, "/:identifier/milestones/:milestone_id", c.Get, public("Get a milestone", "milestones.get", "With progress."))
	fiberoapi.Patch(repoGrp, "/:identifier/milestones/:milestone_id", c.Update, private("Edit, close or reopen a milestone", "milestones.update", "Maintainers and owners."))
	fiberoapi.Delete(repoGrp, "/:identifier/milestones/:milestone_id", c.Delete, private("Delete a milestone", "milestones.delete", "Maintainers and owners. Its issues and pull requests keep going without one."))
}

func ToMilestone(v application.MilestoneView) Milestone {
	return Milestone{Id: v.Id, Title: v.Title, Description: v.Description, DueOn: v.DueOn, State: string(v.State), ClosedAt: v.ClosedAt,
		Open: v.Progress.Open, Closed: v.Progress.Closed, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
}

func change(f Fields) application.MilestoneChange {
	return application.MilestoneChange{Title: f.Title, Description: f.Description, DueOn: f.DueOn, ClearDueOn: f.ClearDueOn, State: f.State}
}

func authed(ctx *fiber.Ctx) (string, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", apierror.Unauthenticated()
	}
	return auth.UserID, nil
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	views, err := c.Milestones.List(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), req.State)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ListResponse{Milestones: make([]Milestone, 0, len(views))}
	for _, v := range views {
		resp.Milestones = append(resp.Milestones, ToMilestone(v))
	}
	return resp, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Milestone, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Milestones.Create(id, path, user, change(req.Fields))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := ToMilestone(*v)
	return &out, nil
}

func (c Controller) Get(ctx *fiber.Ctx, req IdRequest) (*Milestone, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	v, err := c.Milestones.Get(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), req.MilestoneId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := ToMilestone(*v)
	return &out, nil
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*Milestone, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Milestones.Update(id, path, user, req.MilestoneId, change(req.Fields))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := ToMilestone(*v)
	return &out, nil
}

func (c Controller) Delete(ctx *fiber.Ctx, req IdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Milestones.Delete(id, path, user, req.MilestoneId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "milestone deleted"}, nil
}
