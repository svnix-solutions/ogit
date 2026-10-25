package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

func setupBranchProtectionRoutes(controller Controller) {
	tags := []string{"Branch protection"}
	fiberoapi.Get(controller.FiberOapi, "/:identifier/branch-protection", controller.ListBranchProtection, fiberoapi.OpenAPIOptions{
		Summary: "List branch protection rules", Description: "Developers and up.", OperationID: "repository.protection.list", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/branch-protection", controller.CreateBranchProtection, fiberoapi.OpenAPIOptions{
		Summary: "Protect branches",
		Description: "Owners. pattern is a branch name or a glob (* within a segment, ** across, ?); the most specific rule wins. " +
			"Protected branches refuse force-pushes and deletion unless allowed. require_pull_request refuses direct pushes; " +
			"allowed_users / allowed_groups (when set) are the only ones who may push or merge; required_approvals (-1: the repository's) and " +
			"dismiss_stale_approvals apply to merges; owners_bypass lets owners break the rule (logged).",
		OperationID: "repository.protection.create", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/branch-protection/:rule_id", controller.GetBranchProtection, fiberoapi.OpenAPIOptions{
		Summary: "Get a branch protection rule", OperationID: "repository.protection.get", Tags: tags,
	})
	fiberoapi.Patch(controller.FiberOapi, "/:identifier/branch-protection/:rule_id", controller.UpdateBranchProtection, fiberoapi.OpenAPIOptions{
		Summary: "Change a branch protection rule", Description: "Owners. Omitted fields are kept.", OperationID: "repository.protection.update", Tags: tags,
	})
	fiberoapi.Delete(controller.FiberOapi, "/:identifier/branch-protection/:rule_id", controller.DeleteBranchProtection, fiberoapi.OpenAPIOptions{
		Summary: "Remove a branch protection rule", Description: "Owners.", OperationID: "repository.protection.delete", Tags: tags,
	})
}

func ruleResponse(r *application.RuleView) dtos.BranchProtectionResponse {
	return dtos.BranchProtectionResponse{
		Id: r.Id, Pattern: r.Pattern, AllowForcePush: r.AllowForcePush, AllowDeletion: r.AllowDeletion,
		RequirePullRequest: r.RequirePullRequest, RequiredApprovals: r.RequiredApprovals, DismissStaleApprovals: r.DismissStaleApprovals,
		AllowedUsers: r.AllowedUsers, AllowedGroups: r.AllowedGroups, OwnersBypass: r.OwnersBypass, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func ruleChange(f dtos.BranchProtectionFields) application.RuleChange {
	return application.RuleChange{
		Pattern: f.Pattern, AllowForcePush: f.AllowForcePush, AllowDeletion: f.AllowDeletion, RequirePullRequest: f.RequirePullRequest,
		RequiredApprovals: f.RequiredApprovals, DismissStaleApprovals: f.DismissStaleApprovals,
		AllowedUsers: f.AllowedUsers, AllowedGroups: f.AllowedGroups, OwnersBypass: f.OwnersBypass,
	}
}

func (ctrl *Controller) ListBranchProtection(ctx *fiber.Ctx, req dtos.ListBranchProtectionRequest) (*dtos.ListBranchProtectionResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rules, err := ctrl.ProtectionApp.List(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListBranchProtectionResponse{Rules: make([]dtos.BranchProtectionResponse, 0, len(rules))}
	for i := range rules {
		resp.Rules = append(resp.Rules, ruleResponse(&rules[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateBranchProtection(ctx *fiber.Ctx, req dtos.CreateBranchProtectionRequest) (*dtos.BranchProtectionResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if req.Pattern == nil {
		empty := ""
		req.Pattern = &empty
	}
	rule, err := ctrl.ProtectionApp.Create(id, path, authCtx.UserID, ruleChange(req.BranchProtectionFields))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := ruleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) GetBranchProtection(ctx *fiber.Ctx, req dtos.BranchProtectionRequest) (*dtos.BranchProtectionResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rule, err := ctrl.ProtectionApp.Get(id, path, authCtx.UserID, req.RuleId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := ruleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) UpdateBranchProtection(ctx *fiber.Ctx, req dtos.UpdateBranchProtectionRequest) (*dtos.BranchProtectionResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rule, err := ctrl.ProtectionApp.Update(id, path, authCtx.UserID, req.RuleId, ruleChange(req.BranchProtectionFields))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := ruleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) DeleteBranchProtection(ctx *fiber.Ctx, req dtos.BranchProtectionRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.ProtectionApp.Delete(id, path, authCtx.UserID, req.RuleId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "rule deleted"}, nil
}
