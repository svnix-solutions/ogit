package repository

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

// SetupGroupWebhooks serves a group's hooks under /group/:identifier/hooks
// (the same API as a repository's; they fire for every repository in the
// group and its sub-groups).
func SetupGroupWebhooks(c Controller, groupGrp *fiberoapi.OApiGroup) {
	tags := []string{"Webhooks"}
	who := "The group's maintainers and owners. Group hooks fire for every repository in the group and its sub-groups."
	fiberoapi.Get(groupGrp, "/:identifier/hooks", c.ListGroupWebhooks, fiberoapi.OpenAPIOptions{Summary: "List group webhooks", Description: who, OperationID: "group.hooks.list", Tags: tags})
	fiberoapi.Post(groupGrp, "/:identifier/hooks", c.CreateGroupWebhook, fiberoapi.OpenAPIOptions{Summary: "Create group webhook", Description: who + " Same fields as a repository's.", OperationID: "group.hooks.create", Tags: tags})
	fiberoapi.Get(groupGrp, "/:identifier/hooks/:hook_id", c.GetGroupWebhook, fiberoapi.OpenAPIOptions{Summary: "Get group webhook", OperationID: "group.hooks.get", Tags: tags})
	fiberoapi.Patch(groupGrp, "/:identifier/hooks/:hook_id", c.UpdateGroupWebhook, fiberoapi.OpenAPIOptions{Summary: "Update group webhook", OperationID: "group.hooks.update", Tags: tags})
	fiberoapi.Delete(groupGrp, "/:identifier/hooks/:hook_id", c.DeleteGroupWebhook, fiberoapi.OpenAPIOptions{Summary: "Delete group webhook", OperationID: "group.hooks.delete", Tags: tags})
	fiberoapi.Post(groupGrp, "/:identifier/hooks/:hook_id/pings", c.PingGroupWebhook, fiberoapi.OpenAPIOptions{Summary: "Ping group webhook", OperationID: "group.hooks.ping", Tags: tags})
	fiberoapi.Get(groupGrp, "/:identifier/hooks/:hook_id/deliveries", c.ListGroupDeliveries, fiberoapi.OpenAPIOptions{Summary: "List group webhook deliveries", OperationID: "group.hooks.deliveries.list", Tags: tags})
	fiberoapi.Get(groupGrp, "/:identifier/hooks/:hook_id/deliveries/:delivery_id", c.GetGroupDelivery, fiberoapi.OpenAPIOptions{Summary: "Get group webhook delivery", OperationID: "group.hooks.deliveries.get", Tags: tags})
	fiberoapi.Post(groupGrp, "/:identifier/hooks/:hook_id/deliveries/:delivery_id/attempts", c.RedeliverGroup, fiberoapi.OpenAPIOptions{Summary: "Redeliver (group webhook)", OperationID: "group.hooks.deliveries.redeliver", Tags: tags})
}

func (ctrl *Controller) groupScope(ctx *fiber.Ctx, identifier string) (string, application.WebhookScope, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", application.WebhookScope{}, apierror.Unauthenticated()
	}
	s, err := ctrl.WebhookApp.GroupHooks(identifier, authCtx.UserID)
	if err != nil {
		return "", s, apierror.From(err, ctrl.Logger)
	}
	return authCtx.UserID, s, nil
}

func (ctrl *Controller) groupHook(ctx *fiber.Ctx, identifier, hookId string) (string, application.WebhookScope, *domain.Webhook, *fiberoapi.ErrorResponse) {
	user, s, e := ctrl.groupScope(ctx, identifier)
	if e != nil {
		return "", s, nil, e
	}
	h, err := ctrl.WebhookApp.HookIn(s, hookId)
	if err != nil {
		return "", s, nil, apierror.From(err, ctrl.Logger)
	}
	return user, s, h, nil
}

func (ctrl *Controller) ListGroupWebhooks(ctx *fiber.Ctx, req dtos.ListWebhooksRequest) (*dtos.ListWebhooksResponse, *fiberoapi.ErrorResponse) {
	_, s, e := ctrl.groupScope(ctx, req.Identifier)
	if e != nil {
		return nil, e
	}
	hooks, err := ctrl.WebhookApp.ListIn(s)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListWebhooksResponse{Webhooks: make([]dtos.WebhookResponse, 0, len(hooks))}
	for i := range hooks {
		resp.Webhooks = append(resp.Webhooks, webhookResponse(&hooks[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateGroupWebhook(ctx *fiber.Ctx, req dtos.CreateWebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	user, s, e := ctrl.groupScope(ctx, req.Identifier)
	if e != nil {
		return nil, e
	}
	h, err := ctrl.WebhookApp.CreateIn(s, user, application.WebhookChange{URL: &req.URL, Secret: &req.Secret, Events: req.Events, Active: req.Active})
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) GetGroupWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) UpdateGroupWebhook(ctx *fiber.Ctx, req dtos.UpdateWebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	h, err := ctrl.WebhookApp.UpdateHook(h, application.WebhookChange{URL: req.URL, Secret: req.Secret, Events: req.Events, Active: req.Active})
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) DeleteGroupWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	if err := ctrl.WebhookApp.Hooks.Delete(h.Id); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "webhook deleted"}, nil
}

func (ctrl *Controller) PingGroupWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	user, s, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	if err := ctrl.WebhookApp.PingHook(s, h, user); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "ping queued"}, nil
}

func (ctrl *Controller) ListGroupDeliveries(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.ListDeliveriesResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	ds, err := ctrl.WebhookApp.HookDeliveries(h)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListDeliveriesResponse{Deliveries: make([]dtos.DeliveryResponse, 0, len(ds))}
	for i := range ds {
		resp.Deliveries = append(resp.Deliveries, deliveryResponse(&ds[i], false))
	}
	return resp, nil
}

func (ctrl *Controller) GetGroupDelivery(ctx *fiber.Ctx, req dtos.DeliveryRequest) (*dtos.DeliveryResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	d, err := ctrl.WebhookApp.HookDelivery(h, req.DeliveryId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := deliveryResponse(d, true)
	return &resp, nil
}

func (ctrl *Controller) RedeliverGroup(ctx *fiber.Ctx, req dtos.DeliveryRequest) (*dtos.DeliveryResponse, *fiberoapi.ErrorResponse) {
	_, _, h, e := ctrl.groupHook(ctx, req.Identifier, req.HookId)
	if e != nil {
		return nil, e
	}
	d, err := ctrl.WebhookApp.RedeliverHook(h, req.DeliveryId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := deliveryResponse(d, false)
	return &resp, nil
}
