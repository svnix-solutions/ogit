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

func setupWebhookRoutes(controller Controller) {
	tags := []string{"Webhooks"}
	fiberoapi.Get(controller.FiberOapi, "/:identifier/hooks", controller.ListWebhooks, fiberoapi.OpenAPIOptions{
		Summary: "List webhooks", Description: "Maintainers and owners.", OperationID: "repository.hooks.list", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/hooks", controller.CreateWebhook, fiberoapi.OpenAPIOptions{
		Summary: "Create webhook",
		Description: "Maintainers and owners. Events: push, pull_request, pull_request_review, pull_request_review_comment, issue_comment, repository, or * (default push). " +
			"Payloads are GitHub-compatible JSON, signed with the secret in X-Hub-Signature-256. A ping is sent at once. " +
			"Private, loopback and link-local addresses are refused unless listed in webhooks.allowed_hosts.",
		OperationID: "repository.hooks.create", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/hooks/:hook_id", controller.GetWebhook, fiberoapi.OpenAPIOptions{
		Summary: "Get webhook", OperationID: "repository.hooks.get", Tags: tags,
	})
	fiberoapi.Patch(controller.FiberOapi, "/:identifier/hooks/:hook_id", controller.UpdateWebhook, fiberoapi.OpenAPIOptions{
		Summary: "Update webhook", Description: "Omitted fields are kept; an empty secret removes it.", OperationID: "repository.hooks.update", Tags: tags,
	})
	fiberoapi.Delete(controller.FiberOapi, "/:identifier/hooks/:hook_id", controller.DeleteWebhook, fiberoapi.OpenAPIOptions{
		Summary: "Delete webhook", Description: "Also deletes its delivery log.", OperationID: "repository.hooks.delete", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/hooks/:hook_id/pings", controller.PingWebhook, fiberoapi.OpenAPIOptions{
		Summary: "Ping webhook", Description: "Queues a ping event.", OperationID: "repository.hooks.ping", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/hooks/:hook_id/deliveries", controller.ListDeliveries, fiberoapi.OpenAPIOptions{
		Summary: "List deliveries", Description: "The newest 100, newest first. Failed deliveries are retried after 30s, 2m, 10m and 30m.", OperationID: "repository.hooks.deliveries.list", Tags: tags,
	})
	fiberoapi.Get(controller.FiberOapi, "/:identifier/hooks/:hook_id/deliveries/:delivery_id", controller.GetDelivery, fiberoapi.OpenAPIOptions{
		Summary: "Get delivery", Description: "With the payload and the start of the response body.", OperationID: "repository.hooks.deliveries.get", Tags: tags,
	})
	fiberoapi.Post(controller.FiberOapi, "/:identifier/hooks/:hook_id/deliveries/:delivery_id/attempts", controller.Redeliver, fiberoapi.OpenAPIOptions{
		Summary: "Redeliver", Description: "Queues the delivery's payload again, as a new delivery.", OperationID: "repository.hooks.deliveries.redeliver", Tags: tags,
	})
}

func webhookResponse(h *domain.Webhook) dtos.WebhookResponse {
	events := []string(h.Events)
	if events == nil {
		events = []string{}
	}
	return dtos.WebhookResponse{Id: h.Id, URL: h.URL, Events: events, Active: h.Active, HasSecret: h.Secret != "", CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt}
}

func deliveryResponse(d *domain.WebhookDelivery, details bool) dtos.DeliveryResponse {
	r := dtos.DeliveryResponse{Id: d.Id, Event: d.Event, Action: d.Action, Status: string(d.Status), Attempts: d.Attempts,
		ResponseStatus: d.ResponseStatus, DurationMs: d.DurationMs, Error: d.Error, CreatedAt: d.CreatedAt, DeliveredAt: d.DeliveredAt}
	if d.Status == domain.DeliveryPending {
		next := d.NextAttemptAt
		r.NextAttemptAt = &next
	}
	if details {
		r.Payload, r.ResponseBody = d.Payload, d.ResponseBody
	}
	return r
}

func (ctrl *Controller) ListWebhooks(ctx *fiber.Ctx, req dtos.ListWebhooksRequest) (*dtos.ListWebhooksResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	hooks, err := ctrl.WebhookApp.List(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListWebhooksResponse{Webhooks: make([]dtos.WebhookResponse, 0, len(hooks))}
	for i := range hooks {
		resp.Webhooks = append(resp.Webhooks, webhookResponse(&hooks[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateWebhook(ctx *fiber.Ctx, req dtos.CreateWebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	h, err := ctrl.WebhookApp.Create(id, path, authCtx.UserID, application.WebhookChange{URL: &req.URL, Secret: &req.Secret, Events: req.Events, Active: req.Active})
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) GetWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	h, err := ctrl.WebhookApp.Get(id, path, authCtx.UserID, req.HookId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) UpdateWebhook(ctx *fiber.Ctx, req dtos.UpdateWebhookRequest) (*dtos.WebhookResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	h, err := ctrl.WebhookApp.Update(id, path, authCtx.UserID, req.HookId, application.WebhookChange{URL: req.URL, Secret: req.Secret, Events: req.Events, Active: req.Active})
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := webhookResponse(h)
	return &resp, nil
}

func (ctrl *Controller) DeleteWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.WebhookApp.Delete(id, path, authCtx.UserID, req.HookId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "webhook deleted"}, nil
}

func (ctrl *Controller) PingWebhook(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.WebhookMessageResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.WebhookApp.Ping(id, path, authCtx.UserID, req.HookId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.WebhookMessageResponse{Message: "ping queued"}, nil
}

func (ctrl *Controller) ListDeliveries(ctx *fiber.Ctx, req dtos.WebhookRequest) (*dtos.ListDeliveriesResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	ds, err := ctrl.WebhookApp.Deliveries(id, path, authCtx.UserID, req.HookId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListDeliveriesResponse{Deliveries: make([]dtos.DeliveryResponse, 0, len(ds))}
	for i := range ds {
		resp.Deliveries = append(resp.Deliveries, deliveryResponse(&ds[i], false))
	}
	return resp, nil
}

func (ctrl *Controller) GetDelivery(ctx *fiber.Ctx, req dtos.DeliveryRequest) (*dtos.DeliveryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	d, err := ctrl.WebhookApp.Delivery(id, path, authCtx.UserID, req.HookId, req.DeliveryId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := deliveryResponse(d, true)
	return &resp, nil
}

func (ctrl *Controller) Redeliver(ctx *fiber.Ctx, req dtos.DeliveryRequest) (*dtos.DeliveryResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	d, err := ctrl.WebhookApp.Redeliver(id, path, authCtx.UserID, req.HookId, req.DeliveryId)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := deliveryResponse(d, false)
	return &resp, nil
}
