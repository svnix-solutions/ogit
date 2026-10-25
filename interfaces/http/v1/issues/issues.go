// Package issues serves a repository's issues under
// /api/v1/repository/:identifier/issues.
package issues

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	Issues     *application.IssueApp
}

type Label struct {
	Id          string `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type Summary struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`  // open or closed
	Author    string     `json:"author"` // "" once the account is deleted
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	Labels    []Label    `json:"labels"`
}

type Detail struct {
	Summary
	Body       string `json:"body"`
	ClosedBy   string `json:"closed_by,omitempty"`
	CanEdit    bool   `json:"can_edit"`    // title, body, close and reopen
	CanComment bool   `json:"can_comment"` // signed in, issues on, not archived
	CanLabel   bool   `json:"can_label"`
	CanDelete  bool   `json:"can_delete"`
}

type Comment struct {
	Id        string    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CanEdit   bool      `json:"can_edit"`
	CanDelete bool      `json:"can_delete"`
}

type ListRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	State      string `query:"state" json:"-"` // open (default), closed or all
	Label      string `query:"label" json:"-"`
	Author     string `query:"author" json:"-"` // username
	Query      string `query:"q" json:"-"`      // in titles and bodies
	Offset     int    `query:"offset" json:"-"`
	Limit      int    `query:"limit" json:"-"`
}

type ListResponse struct {
	Issues []Summary        `json:"issues"`
	Counts map[string]int64 `json:"counts"` // per state, with the other filters
}

type CreateRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Title      string `json:"title" validate:"required"`
	Body       string `json:"body"`
}

type NumberRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
}

type UpdateRequest struct {
	Identifier string  `path:"identifier" json:"-" validate:"required"`
	Number     string  `path:"number" json:"-" validate:"required"`
	Title      *string `json:"title"`
	Body       *string `json:"body"`
	State      *string `json:"state"` // open or closed
}

type CommentsResponse struct {
	Comments []Comment `json:"comments"`
}

type CommentRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
	Body       string `json:"body" validate:"required"`
}

type CommentIdRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
	CommentId  string `path:"comment_id" json:"-" validate:"required"`
	Body       string `json:"body"`
}

type LabelsRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	Number     string   `path:"number" json:"-" validate:"required"`
	Labels     []string `json:"labels"`
}

type LabelsResponse struct {
	Labels []Label `json:"labels"`
}

type ResolveResponse struct {
	Type string `json:"type"` // issue or pull
}

type MessageResponse struct {
	Message string `json:"message"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Issues"}
	public := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled", Description: desc + " No session needed for public repositories."}
	}
	private := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	fiberoapi.Get(repoGrp, "/:identifier/issues", c.List, public("List issues", "issues.list", "?state=open (default), closed or all; ?label=, ?author=, ?q= (title and body); newest first, with counts per state."))
	fiberoapi.Post(repoGrp, "/:identifier/issues", c.Create, private("Open an issue", "issues.create", "Anyone who can read the repository. Numbers are shared with pull requests."))
	fiberoapi.Get(repoGrp, "/:identifier/issues/:number", c.Get, public("Get an issue", "issues.get", "With what the caller may do."))
	fiberoapi.Patch(repoGrp, "/:identifier/issues/:number", c.Update, private("Edit, close or reopen an issue", "issues.update", "The author and developers and up: title, body, state (open or closed)."))
	fiberoapi.Delete(repoGrp, "/:identifier/issues/:number", c.Delete, private("Delete an issue", "issues.delete", "Maintainers and owners."))
	fiberoapi.Get(repoGrp, "/:identifier/issues/:number/comments", c.Comments, public("Issue comments", "issues.comments", "Oldest first."))
	fiberoapi.Post(repoGrp, "/:identifier/issues/:number/comments", c.AddComment, private("Comment on an issue", "issues.comment", "Anyone who can read the repository."))
	fiberoapi.Patch(repoGrp, "/:identifier/issues/:number/comments/:comment_id", c.EditComment, private("Edit an issue comment", "issues.comment.edit", "Its author."))
	fiberoapi.Delete(repoGrp, "/:identifier/issues/:number/comments/:comment_id", c.DeleteComment, private("Delete an issue comment", "issues.comment.delete", "Its author, or a maintainer."))
	fiberoapi.Put(repoGrp, "/:identifier/issues/:number/labels", c.SetLabels, private("Set an issue's labels", "issues.labels", "Developers and up. Replaces the labels by name."))
	fiberoapi.Get(repoGrp, "/:identifier/numbers/:number", c.Resolve, public("Issue or pull request?", "numbers.resolve", "Whether #number is an issue or a pull request (they share numbers)."))
}

func (c Controller) userId(ctx *fiber.Ctx) string { return optauth.UserID(ctx, c.SessionApp) }

func authed(ctx *fiber.Ctx) (string, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", apierror.Unauthenticated()
	}
	return auth.UserID, nil
}

// number parses :number (fiber-oapi fills strings only).
func number(s string) (int, *fiberoapi.ErrorResponse) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 0, apierror.From(application.ErrIssueNotFound, zerolog.Nop())
	}
	return n, nil
}

func labels(ls []domain.Label) []Label {
	out := make([]Label, 0, len(ls))
	for _, l := range ls {
		out = append(out, Label{Id: l.Id, Name: l.Name, Color: l.Color, Description: l.Description})
	}
	return out
}

func username(u *domain.User) string {
	if u == nil {
		return ""
	}
	return u.Username
}

func summary(is *domain.Issue, ls []domain.Label) Summary {
	return Summary{Number: is.Number, Title: is.Title, State: string(is.State), Author: username(is.Author),
		CreatedAt: is.CreatedAt, UpdatedAt: is.UpdatedAt, ClosedAt: is.ClosedAt, Labels: labels(ls)}
}

func detail(v *application.IssueView) *Detail {
	return &Detail{Summary: summary(v.Issue, v.Labels), Body: v.Body, ClosedBy: username(v.ClosedBy),
		CanEdit: v.CanEdit, CanComment: v.CanComment, CanLabel: v.CanLabel, CanDelete: v.CanDelete}
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	list, err := c.Issues.List(id, path, c.userId(ctx), application.IssueQuery{State: req.State, Label: req.Label, Author: req.Author, Query: req.Query}, req.Offset, req.Limit)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ListResponse{Issues: make([]Summary, 0, len(list.Issues)), Counts: map[string]int64{"open": 0, "closed": 0}}
	for i := range list.Issues {
		resp.Issues = append(resp.Issues, summary(&list.Issues[i], list.Labels[list.Issues[i].Id]))
	}
	for s, n := range list.Counts {
		resp.Counts[string(s)] = n
	}
	return resp, nil
}

func (c Controller) Create(ctx *fiber.Ctx, req CreateRequest) (*Detail, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Issues.Create(id, path, user, req.Title, req.Body)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(v), nil
}

func (c Controller) Get(ctx *fiber.Ctx, req NumberRequest) (*Detail, *fiberoapi.ErrorResponse) {
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Issues.Get(id, path, c.userId(ctx), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(v), nil
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*Detail, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Issues.Update(id, path, user, n, application.IssueUpdate{Title: req.Title, Body: req.Body, State: req.State})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(v), nil
}

func (c Controller) Delete(ctx *fiber.Ctx, req NumberRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Issues.Delete(id, path, user, n); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "issue deleted"}, nil
}

func (c Controller) Comments(ctx *fiber.Ctx, req NumberRequest) (*CommentsResponse, *fiberoapi.ErrorResponse) {
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, comments, err := c.Issues.Comments(id, path, c.userId(ctx), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &CommentsResponse{Comments: make([]Comment, 0, len(comments))}
	for _, cm := range comments {
		mine := v.Viewer() != "" && cm.AuthorId != nil && *cm.AuthorId == v.Viewer()
		resp.Comments = append(resp.Comments, Comment{Id: cm.Id, Author: username(cm.Author), Body: cm.Body, CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt,
			CanEdit: v.CanComment && mine, CanDelete: v.CanComment && (mine || v.Role.CanAdmin())})
	}
	return resp, nil
}

func (c Controller) AddComment(ctx *fiber.Ctx, req CommentRequest) (*Comment, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	cm, err := c.Issues.AddComment(id, path, user, n, req.Body)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Comment{Id: cm.Id, Body: cm.Body, CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt, CanEdit: true, CanDelete: true}, nil
}

func (c Controller) EditComment(ctx *fiber.Ctx, req CommentIdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Issues.EditComment(id, path, user, n, req.CommentId, req.Body); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "comment updated"}, nil
}

func (c Controller) DeleteComment(ctx *fiber.Ctx, req CommentIdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Issues.DeleteComment(id, path, user, n, req.CommentId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "comment deleted"}, nil
}

func (c Controller) SetLabels(ctx *fiber.Ctx, req LabelsRequest) (*LabelsResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	set, err := c.Issues.SetLabels(id, path, user, n, req.Labels)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &LabelsResponse{Labels: labels(set)}, nil
}

func (c Controller) Resolve(ctx *fiber.Ctx, req NumberRequest) (*ResolveResponse, *fiberoapi.ErrorResponse) {
	n, err := strconv.Atoi(req.Number)
	if err != nil || n < 1 {
		return nil, apierror.From(application.ErrNumberNotFound, c.Logger)
	}
	id, path := repoid.Split(req.Identifier)
	t, err := c.Issues.Resolve(id, path, c.userId(ctx), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ResolveResponse{Type: t}, nil
}
