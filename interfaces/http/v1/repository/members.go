package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

// ListRepositories returns the repositories the caller is a member of
// (directly or through a group), or with ?scope=public all public
// repositories, optionally filtered by ?q=.
func (ctrl *Controller) ListRepositories(ctx *fiber.Ctx, req dtos.ListRepositoriesRequest) (*dtos.ListRepositoriesResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	var repos []domain.Repository
	resp := &dtos.ListRepositoriesResponse{}
	switch req.Scope {
	case "", "mine":
		repos, err = ctrl.RepositoryApp.ListForUser(authCtx.UserID)
	case "public":
		var total int64
		repos, total, err = ctrl.RepositoryApp.ListPublic(req.Query, req.Limit, req.Offset)
		resp.Total = &total
	default:
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusBadRequest, Details: "scope must be mine or public", Type: "INVALID_SCOPE"}
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}

	resp.Repositories = make([]dtos.RepositorySummary, 0, len(repos))
	for _, r := range repos {
		resp.Repositories = append(resp.Repositories, dtos.RepositorySummary{
			Id: r.Id, Namespace: r.Namespace, Slug: r.Slug, FullPath: r.FullPath(), Name: r.Name, Description: r.Description,
			IsPrivate: r.IsPrivate, IsArchived: r.IsArchived,
		})
	}
	return resp, nil
}

func (ctrl *Controller) UpdateRepository(ctx *fiber.Ctx, req dtos.UpdateRepositoryRequest) (*dtos.GetRepositoryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, slug := repoid.Split(req.Identifier)
	upd := application.RepositoryUpdate{
		Name: req.Name, Description: req.Description, IsPrivate: req.IsPrivate, Archived: req.Archived,
		RequiredApprovals: req.RequiredApprovals, HasIssues: req.HasIssues,
	}
	if req.WikiAccess != nil {
		access := domain.WikiAccess(*req.WikiAccess)
		upd.WikiAccess = &access
	}
	repo, err := ctrl.RepositoryApp.Update(id, slug, authCtx.UserID, upd)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return repositoryResponse(repo), nil
}

func repositoryResponse(repo *domain.Repository) *dtos.GetRepositoryResponse {
	return &dtos.GetRepositoryResponse{Id: repo.Id, Namespace: repo.Namespace, OwnerType: string(repo.OwnerType), Slug: repo.Slug,
		FullPath: repo.FullPath(), Name: repo.Name, Description: repo.Description, IsPrivate: repo.IsPrivate, IsArchived: repo.IsArchived,
		IsMirror: repo.IsMirror, RequiredApprovals: repo.RequiredApprovals, HasIssues: repo.HasIssues, WikiAccess: string(repo.WikiAccess)}
}

func (ctrl *Controller) DeleteRepository(ctx *fiber.Ctx, req dtos.DeleteRepositoryRequest) (*dtos.DeleteRepositoryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, slug := repoid.Split(req.Identifier)
	if err := ctrl.RepositoryApp.Delete(id, slug, authCtx.UserID, req.Confirm); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.DeleteRepositoryResponse{Message: "repository deleted"}, nil
}

func (ctrl *Controller) TransferRepository(ctx *fiber.Ctx, req dtos.TransferRepositoryRequest) (*dtos.GetRepositoryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	repo, err := ctrl.RepositoryApp.Transfer(id, path, authCtx.UserID, req.Namespace)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return repositoryResponse(repo), nil
}

func (ctrl *Controller) ListMembers(ctx *fiber.Ctx, req dtos.ListMembersRequest) (*dtos.ListMembersResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	id, slug := repoid.Split(req.Identifier)
	members, err := ctrl.RepositoryApp.ListMembers(id, slug, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}

	resp := &dtos.ListMembersResponse{Members: make([]dtos.MemberResponse, 0, len(members))}
	for i := range members {
		resp.Members = append(resp.Members, memberResponse(&members[i]))
	}
	return resp, nil
}

func (ctrl *Controller) AddMember(ctx *fiber.Ctx, req dtos.AddMemberRequest) (*dtos.MemberResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	id, slug := repoid.Split(req.Identifier)
	member, err := ctrl.RepositoryApp.AddMember(id, slug, authCtx.UserID, application.AddMemberInput{
		Username: req.Username,
		Group:    req.Group,
		Role:     domain.AccessType(req.Role),
	})
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}

	resp := memberResponse(member)
	return &resp, nil
}

func (ctrl *Controller) UpdateMember(ctx *fiber.Ctx, req dtos.UpdateMemberRequest) (*dtos.MemberResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	id, slug := repoid.Split(req.Identifier)
	member, err := ctrl.RepositoryApp.UpdateMember(id, slug, authCtx.UserID, req.MemberId, domain.AccessType(req.Role))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}

	resp := memberResponse(member)
	return &resp, nil
}

func (ctrl *Controller) RemoveMember(ctx *fiber.Ctx, req dtos.RemoveMemberRequest) (*dtos.RemoveMemberResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	id, slug := repoid.Split(req.Identifier)
	if err := ctrl.RepositoryApp.RemoveMember(id, slug, authCtx.UserID, req.MemberId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.RemoveMemberResponse{Message: "member removed"}, nil
}

// splitIdentifier tells a repository UUID from a slug.
func memberResponse(m *domain.RepositoryMember) dtos.MemberResponse {
	resp := dtos.MemberResponse{Id: m.Id, Type: string(m.MemberType), Role: string(m.AccessType)}
	switch m.MemberType {
	case domain.MemberTypeUser:
		resp.Username = m.User.Username
	case domain.MemberTypeGroup:
		resp.Group = m.Group.Name
	}
	return resp
}
