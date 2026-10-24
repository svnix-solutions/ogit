package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

// ListRepositories returns the repositories the caller is a member of,
// directly or through a group.
func (ctrl *Controller) ListRepositories(ctx *fiber.Ctx, _ struct{}) (*dtos.ListRepositoriesResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	repos, err := ctrl.RepositoryApp.ListForUser(authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}

	resp := &dtos.ListRepositoriesResponse{Repositories: make([]dtos.RepositorySummary, 0, len(repos))}
	for _, r := range repos {
		resp.Repositories = append(resp.Repositories, dtos.RepositorySummary{
			Id: r.Id, Slug: r.Slug, Name: r.Name, Description: r.Description, IsPrivate: r.IsPrivate,
		})
	}
	return resp, nil
}

func (ctrl *Controller) ListMembers(ctx *fiber.Ctx, req dtos.ListMembersRequest) (*dtos.ListMembersResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}

	id, slug := splitIdentifier(req.Identifier)
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

	id, slug := splitIdentifier(req.Identifier)
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

	id, slug := splitIdentifier(req.Identifier)
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

	id, slug := splitIdentifier(req.Identifier)
	if err := ctrl.RepositoryApp.RemoveMember(id, slug, authCtx.UserID, req.MemberId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.RemoveMemberResponse{Message: "member removed"}, nil
}

// splitIdentifier tells a repository UUID from a slug.
func splitIdentifier(identifier string) (id, slug *string) {
	if validator.IsValidUUID(identifier) {
		return &identifier, nil
	}
	return nil, &identifier
}

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
