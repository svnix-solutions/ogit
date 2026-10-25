package dtos

import "time"

type WebhookResponse struct {
	Id        string    `json:"id"`
	URL       string    `json:"url"`
	Events    []string  `json:"events"`
	Active    bool      `json:"active"`
	HasSecret bool      `json:"has_secret"` // the secret itself is never returned
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ListWebhooksRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type ListWebhooksResponse struct {
	Webhooks []WebhookResponse `json:"webhooks"`
}

type CreateWebhookRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	URL        string   `json:"url" validate:"required"`
	Secret     string   `json:"secret"`
	Events     []string `json:"events"` // default ["push"]; "*" is every event
	Active     *bool    `json:"active"` // default true
}

type WebhookRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	HookId     string `path:"hook_id" json:"-" validate:"required"`
}

type UpdateWebhookRequest struct {
	Identifier string   `path:"identifier" json:"-" validate:"required"`
	HookId     string   `path:"hook_id" json:"-" validate:"required"`
	URL        *string  `json:"url"`
	Secret     *string  `json:"secret"` // "" removes it
	Events     []string `json:"events"`
	Active     *bool    `json:"active"`
}

type WebhookMessageResponse struct {
	Message string `json:"message"`
}

type DeliveryResponse struct {
	Id             string     `json:"id"`
	Event          string     `json:"event"`
	Action         string     `json:"action,omitempty"`
	Status         string     `json:"status"` // pending, delivered or failed
	Attempts       int        `json:"attempts"`
	ResponseStatus int        `json:"response_status,omitempty"`
	DurationMs     int64      `json:"duration_ms"`
	Error          string     `json:"error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"` // pending only
	// Payload and ResponseBody are only in a single delivery's details.
	Payload      string `json:"payload,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
}

type ListDeliveriesResponse struct {
	Deliveries []DeliveryResponse `json:"deliveries"`
}

type DeliveryRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	HookId     string `path:"hook_id" json:"-" validate:"required"`
	DeliveryId string `path:"delivery_id" json:"-" validate:"required"`
}
