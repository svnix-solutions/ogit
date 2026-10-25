package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/webhook"
	"github.com/rs/zerolog"
)

// Webhook rules:
//   - maintainers and owners manage a repository's hooks; secrets are
//     write-only;
//   - events are queued in the database as deliveries (one per hook), so
//     they survive restarts, and sent by a worker on any server: a
//     delivery is claimed with a lock so only one server sends it;
//   - a delivery succeeds on a 2xx answer; otherwise it is retried after
//     30s, 2m, 10m and 30m, then marked failed. The newest 100 deliveries
//     of each hook are kept as its log, and any of them can be redelivered;
//   - payloads follow GitHub's, so existing CI integrations work.

var (
	ErrWebhookNotFound  = errors.New("webhook not found")
	ErrDeliveryNotFound = errors.New("delivery not found")
	ErrInvalidWebhook   = errors.New("webhook URL must be http(s)://host/path, and events one of push, pull_request, pull_request_review, pull_request_review_comment, issue_comment, repository or *")
)

// WebhookEvents are the events hooks can subscribe to ("*" is all).
var WebhookEvents = []string{"push", "pull_request", "pull_request_review", "pull_request_review_comment", "issue_comment", "repository"}

// retryAfter[i] is the wait after failed attempt i+1.
var retryAfter = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

const keepDeliveries = 100

type WebhookApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Users        domain.UserPers
	Hooks        domain.WebhookPers
	// Pulls and Labels describe pull requests in synchronize events.
	Pulls  domain.PullRequestPers
	Labels domain.LabelPers
	Sender *webhook.Sender
	now    func() time.Time
}

func (c *WebhookApp) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// manage returns the repository if actorId may manage its hooks.
func (c *WebhookApp) manage(id, path *string, actorId string) (*domain.Repository, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if !role.CanAdmin() {
		return nil, ErrForbidden
	}
	return repo, nil
}

func validEvents(events []string) bool {
	if len(events) == 0 {
		return false
	}
	for _, e := range events {
		if e != "*" && !slices.Contains(WebhookEvents, e) {
			return false
		}
	}
	return true
}

func (c *WebhookApp) List(id, path *string, actorId string) ([]domain.Webhook, error) {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return nil, err
	}
	return c.Hooks.List(repo.Id)
}

// WebhookChange holds a hook's fields; nil fields are kept on update.
type WebhookChange struct {
	URL    *string
	Secret *string
	Events []string
	Active *bool
}

// Create adds a hook and sends it a ping, like GitHub.
func (c *WebhookApp) Create(id, path *string, actorId string, ch WebhookChange) (*domain.Webhook, error) {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return nil, err
	}
	h := &domain.Webhook{RepositoryId: repo.Id, Events: domain.StringList{"push"}, Active: true}
	if ch.URL != nil {
		h.URL = strings.TrimSpace(*ch.URL)
	}
	if ch.Secret != nil {
		h.Secret = *ch.Secret
	}
	if ch.Events != nil {
		h.Events = ch.Events
	}
	if ch.Active != nil {
		h.Active = *ch.Active
	}
	if webhook.ValidateURL(h.URL) != nil || !validEvents(h.Events) {
		return nil, ErrInvalidWebhook
	}
	if err := c.Hooks.Create(h); err != nil {
		return nil, err
	}
	if h.Active {
		if err := c.enqueue([]domain.Webhook{*h}, "ping", "", c.pingPayload(repo, h, actorId)); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func (c *WebhookApp) hook(id, path *string, actorId, hookId string) (*domain.Repository, *domain.Webhook, error) {
	repo, err := c.manage(id, path, actorId)
	if err != nil {
		return nil, nil, err
	}
	h, err := c.Hooks.Get(repo.Id, hookId)
	if isNotFound(err) {
		return nil, nil, ErrWebhookNotFound
	}
	return repo, h, err
}

func (c *WebhookApp) Get(id, path *string, actorId, hookId string) (*domain.Webhook, error) {
	_, h, err := c.hook(id, path, actorId, hookId)
	return h, err
}

func (c *WebhookApp) Update(id, path *string, actorId, hookId string, ch WebhookChange) (*domain.Webhook, error) {
	_, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{"updated_at": c.clock()}
	if ch.URL != nil {
		h.URL = strings.TrimSpace(*ch.URL)
		fields["url"] = h.URL
	}
	if ch.Secret != nil {
		h.Secret = *ch.Secret
		fields["secret"] = h.Secret
	}
	if ch.Events != nil {
		h.Events = ch.Events
		fields["events"] = h.Events
	}
	if ch.Active != nil {
		h.Active = *ch.Active
		fields["active"] = h.Active
	}
	if webhook.ValidateURL(h.URL) != nil || !validEvents(h.Events) {
		return nil, ErrInvalidWebhook
	}
	return h, c.Hooks.Update(h.Id, fields)
}

func (c *WebhookApp) Delete(id, path *string, actorId, hookId string) error {
	_, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return err
	}
	return c.Hooks.Delete(h.Id)
}

// Ping queues a ping for the hook.
func (c *WebhookApp) Ping(id, path *string, actorId, hookId string) error {
	repo, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return err
	}
	return c.enqueue([]domain.Webhook{*h}, "ping", "", c.pingPayload(repo, h, actorId))
}

func (c *WebhookApp) Deliveries(id, path *string, actorId, hookId string) ([]domain.WebhookDelivery, error) {
	_, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return nil, err
	}
	return c.Hooks.Deliveries(h.Id, keepDeliveries)
}

// Delivery returns one delivery, with its payload and response.
func (c *WebhookApp) Delivery(id, path *string, actorId, hookId, deliveryId string) (*domain.WebhookDelivery, error) {
	_, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return nil, err
	}
	d, err := c.Hooks.Delivery(h.Id, deliveryId)
	if isNotFound(err) {
		return nil, ErrDeliveryNotFound
	}
	return d, err
}

// Redeliver queues a delivery's payload again, as a new delivery.
func (c *WebhookApp) Redeliver(id, path *string, actorId, hookId, deliveryId string) (*domain.WebhookDelivery, error) {
	_, h, err := c.hook(id, path, actorId, hookId)
	if err != nil {
		return nil, err
	}
	d, err := c.Hooks.Delivery(h.Id, deliveryId)
	if isNotFound(err) {
		return nil, ErrDeliveryNotFound
	} else if err != nil {
		return nil, err
	}
	again := []domain.WebhookDelivery{{WebhookId: h.Id, Event: d.Event, Action: d.Action, Payload: d.Payload, Status: domain.DeliveryPending, NextAttemptAt: c.clock()}}
	if err := c.Hooks.Enqueue(again); err != nil {
		return nil, err
	}
	return &again[0], nil
}

// emit queues event for the repository's active hooks that want it. It
// never fails the operation that caused the event: errors are logged.
func (c *WebhookApp) emit(repo *domain.Repository, event, action string, payload any) {
	if c == nil {
		return
	}
	hooks, err := c.Hooks.List(repo.Id)
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.webhook").Msg("failed to list webhooks")
		return
	}
	var targets []domain.Webhook
	for _, h := range hooks {
		if h.Active && h.Wants(event) {
			targets = append(targets, h)
		}
	}
	if err := c.enqueue(targets, event, action, payload); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.webhook").Str("event", event).Msg("failed to queue webhook deliveries")
	}
}

func (c *WebhookApp) enqueue(hooks []domain.Webhook, event, action string, payload any) error {
	if len(hooks) == 0 {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	now := c.clock()
	deliveries := make([]domain.WebhookDelivery, 0, len(hooks))
	for _, h := range hooks {
		deliveries = append(deliveries, domain.WebhookDelivery{
			WebhookId: h.Id, Event: event, Action: action, Payload: string(data), Status: domain.DeliveryPending, NextAttemptAt: now,
		})
	}
	return c.Hooks.Enqueue(deliveries)
}

// DeliverDue sends the deliveries that are due and returns how many it
// attempted. The worker calls it in a loop; tests call it directly.
func (c *WebhookApp) DeliverDue(ctx context.Context) int {
	now := c.clock()
	timeout := c.Config.Webhooks.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	claimed, err := c.Hooks.Claim(now, now.Add(2*timeout+time.Minute), 20)
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.webhook").Msg("failed to claim webhook deliveries")
		return 0
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 5)
	for _, d := range claimed {
		wg.Add(1)
		sem <- struct{}{}
		go func(d domain.WebhookDelivery) {
			defer func() { <-sem; wg.Done() }()
			c.deliver(ctx, d)
		}(d)
	}
	wg.Wait()
	return len(claimed)
}

func (c *WebhookApp) deliver(ctx context.Context, d domain.WebhookDelivery) {
	logger := c.Logger.With().Str("component", "application.webhook").Str("delivery", d.Id).Logger()
	hook, err := c.hookById(d.WebhookId)
	if err != nil {
		_ = c.Hooks.Finish(d.Id, map[string]any{"status": domain.DeliveryFailed, "error": "webhook deleted"})
		return
	}
	if !hook.Active {
		_ = c.Hooks.Finish(d.Id, map[string]any{"status": domain.DeliveryFailed, "error": "webhook inactive"})
		return
	}
	resp := c.Sender.Send(ctx, webhook.Request{URL: hook.URL, Secret: hook.Secret, Event: d.Event, DeliveryId: d.Id, HookId: hook.Id, Payload: []byte(d.Payload)})
	attempts := d.Attempts + 1
	fields := map[string]any{"attempts": attempts, "response_status": resp.Status, "response_body": resp.Body, "duration_ms": resp.Duration.Milliseconds(), "error": ""}
	if resp.Err != nil {
		fields["error"] = resp.Err.Error()
	}
	switch {
	case resp.Err == nil && resp.Status >= 200 && resp.Status < 300:
		now := c.clock()
		fields["status"], fields["delivered_at"] = domain.DeliveryDelivered, now
	case attempts > len(retryAfter):
		fields["status"] = domain.DeliveryFailed
		logger.Warn().Int("attempts", attempts).Msg("webhook delivery failed for good")
	default:
		fields["next_attempt_at"] = c.clock().Add(retryAfter[attempts-1])
	}
	if err := c.Hooks.Finish(d.Id, fields); err != nil {
		logger.Error().Err(err).Msg("failed to record webhook delivery")
	}
}

func (c *WebhookApp) hookById(id string) (*domain.Webhook, error) {
	// Deliveries reference their hook; the repository is not needed here.
	return c.Hooks.Get("", id)
}

// Run sends due deliveries every interval and prunes old ones hourly,
// until ctx ends.
func (c *WebhookApp) Run(ctx context.Context, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for c.DeliverDue(ctx) > 0 && ctx.Err() == nil {
			}
			if time.Since(lastPrune) > time.Hour {
				if err := c.Hooks.Prune(keepDeliveries); err != nil {
					c.Logger.Error().Err(err).Str("component", "application.webhook").Msg("failed to prune webhook deliveries")
				}
				lastPrune = time.Now()
			}
		}
	}
}
