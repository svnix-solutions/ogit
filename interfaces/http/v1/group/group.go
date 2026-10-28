package group

import (
	"strings"

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
	var group *domain.Group
	if req.Parent != "" {
		group, err = ctrl.GroupApp.CreateSubgroup(authCtx.UserID, req.Parent, req.Name, req.Description)
	} else {
		group, err = ctrl.GroupApp.Create(authCtx.UserID, req.Name, req.Description)
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toGroup(group)
	out.Role = string(domain.GroupRoleOwner)
	return &out, nil
}

func (ctrl *Controller) List(ctx *fiber.Ctx, _ struct{}) (*dtos.ListGroupsResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	groups, err := ctrl.GroupApp.MembershipsFor(authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListGroupsResponse{Groups: make([]dtos.GroupResponse, 0, len(groups))}
	for _, g := range groups {
		out := toGroup(&g.Group)
		out.Role = string(g.Role)
		resp.Groups = append(resp.Groups, out)
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
	out := toGroup(group)
	out.Role = string(actor.Role)
	if out.RequireTwoFactorInherited, err = ctrl.GroupApp.InheritedTwoFactor(group); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &out, nil
}

func (ctrl *Controller) Update(ctx *fiber.Ctx, req dtos.UpdateGroupRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, actor, err := ctrl.GroupApp.Get(req.Identifier, authCtx.UserID)
	if err == nil && req.RequireTwoFactor != nil {
		group, err = ctrl.GroupApp.SetRequireTwoFactor(req.Identifier, authCtx.UserID, *req.RequireTwoFactor)
	}
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toGroup(group)
	out.Role = string(actor.Role)
	if out.RequireTwoFactorInherited, err = ctrl.GroupApp.InheritedTwoFactor(group); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &out, nil
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
	plain := make([]domain.GroupMember, 0, len(members))
	for _, m := range members {
		plain = append(plain, m.GroupMember)
	}
	twoFactor, err := ctrl.GroupApp.MembersTwoFactor(req.Identifier, authCtx.UserID, plain)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListMembersResponse{Members: make([]dtos.MemberResponse, 0, len(members))}
	for _, m := range members {
		r := dtos.MemberResponse{Username: m.User.Username, Role: string(m.Role), InheritedFrom: m.InheritedFrom}
		if twoFactor != nil {
			on := twoFactor[m.UserId]
			r.TwoFactorEnabled = &on
		}
		resp.Members = append(resp.Members, r)
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

// toGroup is a group's API form (the caller's role is set by callers).
func toGroup(g *domain.Group) dtos.GroupResponse {
	out := dtos.GroupResponse{Id: g.Id, Name: g.Name, Description: g.Description, RequireTwoFactor: g.RequireTwoFactor}
	if i := strings.LastIndex(g.Name, "/"); i > 0 {
		out.Parent = g.Name[:i]
	}
	return out
}

func (ctrl *Controller) Subgroups(ctx *fiber.Ctx, req dtos.GroupRequest) (*dtos.ListSubgroupsResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	groups, err := ctrl.GroupApp.Subgroups(req.Identifier, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListSubgroupsResponse{Groups: make([]dtos.GroupResponse, 0, len(groups))}
	for i := range groups {
		resp.Groups = append(resp.Groups, toGroup(&groups[i]))
	}
	return resp, nil
}

func (ctrl *Controller) Move(ctx *fiber.Ctx, req dtos.MoveGroupRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, err := ctrl.GroupApp.Move(req.Identifier, authCtx.UserID, req.Parent, req.Name)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toGroup(group)
	out.Role = string(domain.GroupRoleOwner)
	return &out, nil
}

func SetupGroupRouter(c Controller) {
	tags := []string{"Groups"}
	fiberoapi.Post(c.FiberOapi, "", c.Create, fiberoapi.OpenAPIOptions{
		Summary: "Create group", OperationID: "group.create", Tags: tags,
		Description: "Any user can create a group and becomes its owner. Names: lowercase letters, digits, '.', '_', '-'. " +
			"With parent (a group's path or id), a sub-group <parent>/<name>, for the parent's maintainers and owners (at most 5 levels; members of the parent are members of it, with the same role).",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/move", c.Move, fiberoapi.OpenAPIOptions{
		Summary: "Rename or move a group", OperationID: "group.move", Tags: tags,
		Description: "The group's owner (direct or inherited). name: its new last segment; parent: a group you maintain or own, or \"\" for the top level. " +
			"Everything below moves with it, and former paths (of the group, its sub-groups and their repositories) redirect. Top-level groups can't be renamed.",
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier/subgroups", c.Subgroups, fiberoapi.OpenAPIOptions{
		Summary: "List a group's sub-groups", OperationID: "group.subgroups", Tags: tags,
		Description: "Members (direct or inherited) only. Group paths with slashes are URL-encoded in :identifier (org%2Fteam).",
	})
	fiberoapi.Get(c.FiberOapi, "", c.List, fiberoapi.OpenAPIOptions{
		Summary: "List my groups", OperationID: "group.list", Tags: tags,
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier", c.Get, fiberoapi.OpenAPIOptions{
		Summary: "Get group", OperationID: "group.get", Tags: tags,
		Description: "By id or name. Only members can see a group.",
	})
	fiberoapi.Patch(c.FiberOapi, "/:identifier", c.Update, fiberoapi.OpenAPIOptions{
		Summary: "Change a group", OperationID: "group.update", Tags: tags,
		Description: "Owner only. require_two_factor: members without two-factor authentication get no access to the group's repositories through it (and can't create repositories in it); they stay members.",
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
