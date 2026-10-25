package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

func setupProtectedTagRoutes(controller Controller) {
	tags := []string{"Protected tags"}
	fiberoapi.Get(controller.FiberOapi, "/:identifier/protected-tags", controller.ListProtectedTags, fiberoapi.OpenAPIOptions{
		Summary: "List protected tag rules", Description: "Developers and up.", OperationID: "repository.protected_tags.list", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/protected-tags", controller.CreateProtectedTag, fiberoapi.OpenAPIOptions{
		Summary: "Protect tags",
		Description: "Owners. pattern is a tag name or a glob (* within a segment, ** across, ?). Only maintainers and up, and allowed_users / " +
			"allowed_groups, may create matching tags (on pushes and releases); nobody may move or delete them, except owners (logged).",
		OperationID: "repository.protected_tags.create", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/protected-tags/:rule_id", controller.GetProtectedTag, fiberoapi.OpenAPIOptions{
		Summary: "Get a protected tag rule", OperationID: "repository.protected_tags.get", Tags: tags,
	})
	fiberoapi.Patch(controller.FiberOapi, "/:identifier/protected-tags/:rule_id", controller.UpdateProtectedTag, fiberoapi.OpenAPIOptions{
		Summary: "Change a protected tag rule", Description: "Owners. Omitted fields are kept.", OperationID: "repository.protected_tags.update", Tags: tags,
	})
	fiberoapi.Delete(controller.FiberOapi, "/:identifier/protected-tags/:rule_id", controller.DeleteProtectedTag, fiberoapi.OpenAPIOptions{
		Summary: "Remove a protected tag rule", Description: "Owners.", OperationID: "repository.protected_tags.delete", Tags: tags,
	})
}

func tagRuleResponse(r *application.TagRuleView) dtos.ProtectedTagResponse {
	return dtos.ProtectedTagResponse{Id: r.Id, Pattern: r.Pattern, AllowedUsers: r.AllowedUsers, AllowedGroups: r.AllowedGroups,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func tagRuleChange(f dtos.ProtectedTagFields) application.TagRuleChange {
	return application.TagRuleChange{Pattern: f.Pattern, AllowedUsers: f.AllowedUsers, AllowedGroups: f.AllowedGroups}
}

func (ctrl *Controller) ListProtectedTags(ctx *fiber.Ctx, req dtos.ListBranchProtectionRequest) (*dtos.ListProtectedTagsResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rules, err := ctrl.ProtectionApp.ListTagRules(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListProtectedTagsResponse{Rules: make([]dtos.ProtectedTagResponse, 0, len(rules))}
	for i := range rules {
		resp.Rules = append(resp.Rules, tagRuleResponse(&rules[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateProtectedTag(ctx *fiber.Ctx, req dtos.CreateProtectedTagRequest) (*dtos.ProtectedTagResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if req.Pattern == nil {
		empty := ""
		req.Pattern = &empty
	}
	rule, err := ctrl.ProtectionApp.CreateTagRule(id, path, authCtx.UserID, tagRuleChange(req.ProtectedTagFields))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := tagRuleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) GetProtectedTag(ctx *fiber.Ctx, req dtos.ProtectedTagRequest) (*dtos.ProtectedTagResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rule, err := ctrl.ProtectionApp.GetTagRule(id, path, authCtx.UserID, req.RuleId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := tagRuleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) UpdateProtectedTag(ctx *fiber.Ctx, req dtos.UpdateProtectedTagRequest) (*dtos.ProtectedTagResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	rule, err := ctrl.ProtectionApp.UpdateTagRule(id, path, authCtx.UserID, req.RuleId, tagRuleChange(req.ProtectedTagFields))
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := tagRuleResponse(rule)
	return &resp, nil
}

func (ctrl *Controller) DeleteProtectedTag(ctx *fiber.Ctx, req dtos.ProtectedTagRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.ProtectionApp.DeleteTagRule(id, path, authCtx.UserID, req.RuleId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "rule deleted"}, nil
}
