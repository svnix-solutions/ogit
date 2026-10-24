package group

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/group/dtos"
	"github.com/rs/zerolog"
)

type Controller struct {
	Config    config.Config
	Logger    zerolog.Logger
	FiberOapi *fiberoapi.OApiGroup
	GroupApp  *application.GroupApp
}

func (ctrl *Controller) Create(ctx *fiber.Ctx, req dtos.CreateGroupRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, err := ctrl.GroupApp.Create(authCtx.UserID, req.Name, req.Description)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.GroupResponse{Id: group.Id, Name: group.Name, Description: group.Description, Role: string(domain.GroupRoleOwner)}, nil
}

func (ctrl *Controller) List(ctx *fiber.Ctx, _ struct{}) (*dtos.ListGroupsResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	groups, err := ctrl.GroupApp.ListForUser(authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListGroupsResponse{Groups: make([]dtos.GroupResponse, 0, len(groups))}
	for _, g := range groups {
		resp.Groups = append(resp.Groups, dtos.GroupResponse{Id: g.Id, Name: g.Name, Description: g.Description})
	}
	return resp, nil
}

func (ctrl *Controller) Get(ctx *fiber.Ctx, req dtos.GroupRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, actor, err := ctrl.GroupApp.Get(req.Identifier, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.GroupResponse{Id: group.Id, Name: group.Name, Description: group.Description, Role: string(actor.Role)}, nil
}

func (ctrl *Controller) Delete(ctx *fiber.Ctx, req dtos.GroupRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.GroupApp.Delete(req.Identifier, authCtx.UserID); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "group deleted"}, nil
}

func (ctrl *Controller) ListMembers(ctx *fiber.Ctx, req dtos.GroupRequest) (*dtos.ListMembersResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	members, err := ctrl.GroupApp.ListMembers(req.Identifier, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListMembersResponse{Members: make([]dtos.MemberResponse, 0, len(members))}
	for _, m := range members {
		resp.Members = append(resp.Members, dtos.MemberResponse{Username: m.User.Username, Role: string(m.Role)})
	}
	return resp, nil
}

func (ctrl *Controller) AddMember(ctx *fiber.Ctx, req dtos.AddMemberRequest) (*dtos.MemberResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	member, err := ctrl.GroupApp.AddMember(req.Identifier, authCtx.UserID, req.Username, domain.GroupRole(req.Role))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MemberResponse{Username: member.User.Username, Role: string(member.Role)}, nil
}

func (ctrl *Controller) UpdateMember(ctx *fiber.Ctx, req dtos.UpdateMemberRequest) (*dtos.MemberResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	member, err := ctrl.GroupApp.UpdateMemberRole(req.Identifier, authCtx.UserID, req.Username, domain.GroupRole(req.Role))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MemberResponse{Username: member.User.Username, Role: string(member.Role)}, nil
}

func (ctrl *Controller) RemoveMember(ctx *fiber.Ctx, req dtos.MemberRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.GroupApp.RemoveMember(req.Identifier, authCtx.UserID, req.Username); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "member removed"}, nil
}

func (ctrl *Controller) Transfer(ctx *fiber.Ctx, req dtos.TransferRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, err := ctrl.GroupApp.TransferOwnership(req.Identifier, authCtx.UserID, req.Username)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.GroupResponse{Id: group.Id, Name: group.Name, Description: group.Description, Role: string(domain.GroupRoleMaintainer)}, nil
}

func SetupGroupRouter(c Controller) {
	tags := []string{"Groups"}
	fiberoapi.Post(c.FiberOapi, "", c.Create, fiberoapi.OpenAPIOptions{
		Summary: "Create group", OperationID: "group.create", Tags: tags,
		Description: "Any user can create a group and becomes its owner. Names: lowercase letters, digits, '.', '_', '-'.",
	})
	fiberoapi.Get(c.FiberOapi, "", c.List, fiberoapi.OpenAPIOptions{
		Summary: "List my groups", OperationID: "group.list", Tags: tags,
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier", c.Get, fiberoapi.OpenAPIOptions{
		Summary: "Get group", OperationID: "group.get", Tags: tags,
		Description: "By id or name. Only members can see a group.",
	})
	fiberoapi.Delete(c.FiberOapi, "/:identifier", c.Delete, fiberoapi.OpenAPIOptions{
		Summary: "Delete group", OperationID: "group.delete", Tags: tags,
		Description: "Owner only. Also removes the group's access to repositories.",
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier/members", c.ListMembers, fiberoapi.OpenAPIOptions{
		Summary: "List group members", OperationID: "group.members.list", Tags: tags,
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/members", c.AddMember, fiberoapi.OpenAPIOptions{
		Summary: "Add group member", OperationID: "group.members.add", Tags: tags,
		Description: "Owner and maintainers add members; only the owner adds maintainers.",
	})
	fiberoapi.Patch(c.FiberOapi, "/:identifier/members/:username", c.UpdateMember, fiberoapi.OpenAPIOptions{
		Summary: "Change a group member's role", OperationID: "group.members.update", Tags: tags,
		Description: "Owner only: switch between member and maintainer.",
	})
	fiberoapi.Delete(c.FiberOapi, "/:identifier/members/:username", c.RemoveMember, fiberoapi.OpenAPIOptions{
		Summary: "Remove a group member", OperationID: "group.members.remove", Tags: tags,
		Description: "Owner removes anyone; maintainers remove members; anyone can leave except the owner.",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/transfer", c.Transfer, fiberoapi.OpenAPIOptions{
		Summary: "Transfer group ownership", OperationID: "group.transfer", Tags: tags,
		Description: "Owner only. The new owner must be a member; the previous owner becomes a maintainer.",
	})
}
