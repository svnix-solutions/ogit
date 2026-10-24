package pulls

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type Comment struct {
	Id        string    `json:"id"`
	Author    string    `json:"author"`
	ReviewId  string    `json:"review_id,omitempty"`
	Body      string    `json:"body"`
	Path      string    `json:"path,omitempty"`
	Line      int       `json:"line,omitempty"`
	Side      string    `json:"side,omitempty"` // old or new
	CommitId  string    `json:"commit_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CanEdit   bool      `json:"can_edit"`
	CanDelete bool      `json:"can_delete"`
}

type Review struct {
	Id        string    `json:"id"`
	Author    string    `json:"author"`
	State     string    `json:"state"`
	Body      string    `json:"body"`
	CommitId  string    `json:"commit_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ActivityResponse struct {
	Reviews  []Review  `json:"reviews"`
	Comments []Comment `json:"comments"` // conversation and inline, with or without a review
}

type InlineTarget struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
}

type CommentRequest struct {
	Identifier string        `path:"identifier" json:"-" validate:"required"`
	Number     string        `path:"number" json:"-" validate:"required"`
	Body       string        `json:"body" validate:"required"`
	Inline     *InlineTarget `json:"inline"` // a line of the diff
}

type CommentIdRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
	CommentId  string `path:"comment_id" json:"-" validate:"required"`
	Body       string `json:"body"`
}

type ReviewRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Number     string `path:"number" json:"-" validate:"required"`
	State      string `json:"state" validate:"required"` // approved, changes_requested or commented
	Body       string `json:"body"`
	Comments   []struct {
		InlineTarget
		Body string `json:"body"`
	} `json:"comments"`
}

type LabelsRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	Number     string   `path:"number" json:"-" validate:"required"`
	Labels     []string `json:"labels"` // names; replaces the current ones
}

type LabelsResponse struct {
	Labels []Label `json:"labels"`
}

type RepoLabelsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type CreateLabelRequest struct {
	Identifier  string  `path:"identifier" json:"-" validate:"required"`
	Name        *string `json:"name"`
	Color       *string `json:"color"` // #rrggbb
	Description *string `json:"description"`
}

type LabelRequest struct {
	Identifier  string  `path:"identifier" json:"-" validate:"required"`
	LabelId     string  `path:"label_id" json:"-" validate:"required"`
	Name        *string `json:"name"`
	Color       *string `json:"color"` // #rrggbb
	Description *string `json:"description"`
}

type MessageResponse struct {
	Message string `json:"message"`
}

func setupReviews(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Pull requests"}
	opts := func(summary, id, desc string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Description: desc}
	}
	fiberoapi.Get(repoGrp, "/:identifier/pulls/:number/activity", c.Activity, fiberoapi.OpenAPIOptions{Summary: "Reviews and comments", OperationID: "pulls.activity", Tags: tags, Security: "disabled",
		Description: "Oldest first. No session needed for public repositories."})
	fiberoapi.Post(repoGrp, "/:identifier/pulls/:number/comments", c.AddComment, opts("Comment", "pulls.comment", "Anyone who can read the repository. With inline, on a line of the diff (side old or new)."))
	fiberoapi.Patch(repoGrp, "/:identifier/pulls/:number/comments/:comment_id", c.EditComment, opts("Edit a comment", "pulls.comment.edit", "Its author."))
	fiberoapi.Delete(repoGrp, "/:identifier/pulls/:number/comments/:comment_id", c.DeleteComment, opts("Delete a comment", "pulls.comment.delete", "Its author, or a maintainer."))
	fiberoapi.Post(repoGrp, "/:identifier/pulls/:number/reviews", c.Review, opts("Review", "pulls.review", "state: approved, changes_requested or commented, with optional inline comments. Authors only comment on their own pull requests. Approvals count from developers and up."))
	fiberoapi.Put(repoGrp, "/:identifier/pulls/:number/labels", c.SetLabels, opts("Set labels", "pulls.labels", "Developers and up. Replaces the labels by name."))
	fiberoapi.Get(repoGrp, "/:identifier/labels", c.ListLabels, fiberoapi.OpenAPIOptions{Summary: "Labels", OperationID: "labels.list", Tags: tags, Security: "disabled", Description: "The repository's labels."})
	fiberoapi.Post(repoGrp, "/:identifier/labels", c.CreateLabel, opts("Create a label", "labels.create", "Maintainers and owners. name, color (#rrggbb), description."))
	fiberoapi.Patch(repoGrp, "/:identifier/labels/:label_id", c.UpdateLabel, opts("Edit a label", "labels.update", "Maintainers and owners."))
	fiberoapi.Delete(repoGrp, "/:identifier/labels/:label_id", c.DeleteLabel, opts("Delete a label", "labels.delete", "Maintainers and owners. Removes it from pull requests."))
}

func username(u *domain.User) string {
	if u == nil {
		return ""
	}
	return u.Username
}

func (c Controller) Activity(ctx *fiber.Ctx, req NumberRequest) (*ActivityResponse, *fiberoapi.ErrorResponse) {
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	viewer := c.userId(ctx)
	view, reviews, comments, err := c.Pulls.Activity(id, path, viewer, n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ActivityResponse{Reviews: []Review{}, Comments: []Comment{}}
	for _, r := range reviews {
		resp.Reviews = append(resp.Reviews, Review{Id: r.Id, Author: username(r.Author), State: string(r.State), Body: r.Body, CommitId: r.CommitId, CreatedAt: r.CreatedAt})
	}
	for _, cm := range comments {
		own := viewer != "" && cm.AuthorId != nil && *cm.AuthorId == viewer
		out := Comment{Id: cm.Id, Author: username(cm.Author), Body: cm.Body, Path: cm.Path, Line: cm.Line, Side: cm.Side, CommitId: cm.CommitId,
			CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt, CanEdit: own, CanDelete: own || view.Role.CanAdmin()}
		if cm.ReviewId != nil {
			out.ReviewId = *cm.ReviewId
		}
		resp.Comments = append(resp.Comments, out)
	}
	return resp, nil
}

func (c Controller) AddComment(ctx *fiber.Ctx, req CommentRequest) (*Comment, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	var inline *application.InlineTarget
	if req.Inline != nil {
		inline = &application.InlineTarget{Path: req.Inline.Path, Line: req.Inline.Line, Side: req.Inline.Side}
	}
	cm, err := c.Pulls.AddComment(id, path, auth.UserID, n, req.Body, inline)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Comment{Id: cm.Id, Body: cm.Body, Path: cm.Path, Line: cm.Line, Side: cm.Side, CommitId: cm.CommitId, CreatedAt: cm.CreatedAt, UpdatedAt: cm.UpdatedAt, CanEdit: true, CanDelete: true}, nil
}

func (c Controller) EditComment(ctx *fiber.Ctx, req CommentIdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Pulls.EditComment(id, path, auth.UserID, n, req.CommentId, req.Body); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "comment updated"}, nil
}

func (c Controller) DeleteComment(ctx *fiber.Ctx, req CommentIdRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Pulls.DeleteComment(id, path, auth.UserID, n, req.CommentId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "comment deleted"}, nil
}

func (c Controller) Review(ctx *fiber.Ctx, req ReviewRequest) (*Review, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	var inline []application.InlineComment
	for _, ic := range req.Comments {
		inline = append(inline, application.InlineComment{InlineTarget: application.InlineTarget{Path: ic.Path, Line: ic.Line, Side: ic.Side}, Body: ic.Body})
	}
	r, err := c.Pulls.SubmitReview(id, path, auth.UserID, n, domain.ReviewState(req.State), req.Body, inline)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Review{Id: r.Id, State: string(r.State), Body: r.Body, CommitId: r.CommitId, CreatedAt: r.CreatedAt}, nil
}

func (c Controller) SetLabels(ctx *fiber.Ctx, req LabelsRequest) (*LabelsResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	set, err := c.Pulls.SetLabels(id, path, auth.UserID, n, req.Labels)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &LabelsResponse{Labels: labels(set)}, nil
}

func (c Controller) ListLabels(ctx *fiber.Ctx, req RepoLabelsRequest) (*LabelsResponse, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(req.Identifier)
	ls, err := c.Pulls.RepositoryLabels(id, path, c.userId(ctx))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &LabelsResponse{Labels: labels(ls)}, nil
}

func (c Controller) CreateLabel(ctx *fiber.Ctx, req CreateLabelRequest) (*Label, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	l, err := c.Pulls.CreateLabel(id, path, auth.UserID, application.LabelChange{Name: req.Name, Color: req.Color, Description: req.Description})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &labels([]domain.Label{*l})[0], nil
}

func (c Controller) UpdateLabel(ctx *fiber.Ctx, req LabelRequest) (*Label, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	l, err := c.Pulls.UpdateLabel(id, path, auth.UserID, req.LabelId, application.LabelChange{Name: req.Name, Color: req.Color, Description: req.Description})
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &labels([]domain.Label{*l})[0], nil
}

func (c Controller) DeleteLabel(ctx *fiber.Ctx, req LabelRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Pulls.DeleteLabel(id, path, auth.UserID, req.LabelId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "label deleted"}, nil
}
