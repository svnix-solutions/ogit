package pulls

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type Milestone struct {
	Id    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type AssigneesRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	Number     string   `path:"number" json:"-" validate:"required"`
	Assignees  []string `json:"assignees"` // usernames with write access, at most 10
}

type AssigneesResponse struct {
	Assignees []string `json:"assignees"`
}

type ReviewersRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	Number     string   `path:"number" json:"-" validate:"required"`
	Reviewers  []string `json:"reviewers"` // usernames who can read the repository, at most 10
}

type ReviewersResponse struct {
	Reviewers []string `json:"reviewers"`
}

type MilestoneRequest struct {
	Identifier  string `path:"identifier" json:"-" validate:"required"`
	Number      string `path:"number" json:"-" validate:"required"`
	MilestoneId string `json:"milestone_id"` // "" removes it
}

func setupAssignees(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Pull requests"}
	fiberoapi.Put(repoGrp, "/:identifier/pulls/:number/assignees", c.SetAssignees, fiberoapi.OpenAPIOptions{Summary: "Set a pull request's assignees",
		OperationID: "pulls.assignees", Tags: tags, Description: "Developers and up. Usernames of users with write access (at most 10)."})
	fiberoapi.Put(repoGrp, "/:identifier/pulls/:number/reviewers", c.SetReviewers, fiberoapi.OpenAPIOptions{Summary: "Request reviews",
		OperationID: "pulls.reviewers", Tags: tags, Description: "The author or developers and up, on open pull requests. Replaces the requested reviewers (usernames who can read the repository, not the author, at most 10). A request goes away when that person reviews."})
	fiberoapi.Put(repoGrp, "/:identifier/pulls/:number/milestone", c.SetMilestone, fiberoapi.OpenAPIOptions{Summary: "Set a pull request's milestone",
		OperationID: "pulls.milestone", Tags: tags, Description: "Developers and up. milestone_id, or \"\" to remove it."})
}

func usernames(users []domain.User) []string {
	out := make([]string, 0, len(users))
	for _, u := range users {
		out = append(out, u.Username)
	}
	return out
}

func toMilestone(m *domain.Milestone) *Milestone {
	if m == nil {
		return nil
	}
	return &Milestone{Id: m.Id, Title: m.Title, State: string(m.State)}
}

func (c Controller) SetAssignees(ctx *fiber.Ctx, req AssigneesRequest) (*AssigneesResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	users, err := c.Pulls.SetAssignees(id, path, auth.UserID, n, req.Assignees)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &AssigneesResponse{Assignees: usernames(users)}, nil
}

func (c Controller) SetReviewers(ctx *fiber.Ctx, req ReviewersRequest) (*ReviewersResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	users, err := c.Pulls.SetReviewers(id, path, auth.UserID, n, req.Reviewers)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ReviewersResponse{Reviewers: usernames(users)}, nil
}

func (c Controller) SetMilestone(ctx *fiber.Ctx, req MilestoneRequest) (*Detail, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	n, errResp := number(req.Number)
	if errResp != nil {
		return nil, errResp
	}
	id, path := repoid.Split(req.Identifier)
	view, err := c.Pulls.SetMilestone(id, path, auth.UserID, n, req.MilestoneId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return detail(view), nil
}
