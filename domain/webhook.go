package domain

import (
	"database/sql/driver"
	"fmt"
	"time"

	"github.com/goccy/go-json"
)

// Webhook sends a repository's events to a URL (GitHub-compatible
// payloads, signed with Secret).
type Webhook struct {
	Id string `gorm:"primaryKey"`
	// RepositoryId or GroupId: what the hook belongs to (a group's hooks
	// fire for every repository below it).
	RepositoryId *string
	GroupId      *string
	URL          string
	Secret       string // HMAC key; never returned by the API
	Events       StringList
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Webhook) TableName() string { return "webhook" }

// Wants reports whether the hook is subscribed to event ("*" is all).
func (w Webhook) Wants(event string) bool {
	for _, e := range w.Events {
		if e == "*" || e == event {
			return true
		}
	}
	return false
}

// WebhookDelivery is one event for one hook: queued, then sent (with
// retries), and kept as its delivery log.
type WebhookDelivery struct {
	Id             string `gorm:"primaryKey"`
	WebhookId      string
	Event          string
	Action         string
	Payload        string
	Status         DeliveryStatus
	Attempts       int
	NextAttemptAt  time.Time
	LockedUntil    *time.Time // a server is sending it
	ResponseStatus int
	ResponseBody   string // truncated
	DurationMs     int64
	Error          string
	CreatedAt      time.Time
	DeliveredAt    *time.Time
}

func (WebhookDelivery) TableName() string { return "webhook_delivery" }

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryDelivered DeliveryStatus = "delivered"
	DeliveryFailed    DeliveryStatus = "failed" // no attempts left
)

type WebhookPers interface {
	Create(h *Webhook) error
	Get(repositoryId, id string) (*Webhook, error)
	List(repositoryId string) ([]Webhook, error)
	GetForGroup(groupId, id string) (*Webhook, error)
	ListForGroups(groupIds []string) ([]Webhook, error)
	Update(id string, fields map[string]any) error
	Delete(id string) error

	// Enqueue stores deliveries to send.
	Enqueue(deliveries []WebhookDelivery) error
	// Claim takes up to n pending deliveries that are due and not being
	// sent, locking them until lockUntil. Safe across servers.
	Claim(now, lockUntil time.Time, n int) ([]WebhookDelivery, error)
	// Finish records an attempt's result and unlocks the delivery.
	Finish(id string, fields map[string]any) error
	Deliveries(webhookId string, limit int) ([]WebhookDelivery, error)
	Delivery(webhookId, id string) (*WebhookDelivery, error)
	// Prune keeps the newest keep deliveries of each hook.
	Prune(keep int) error
}

// StringList is a JSON array column.
type StringList []string

func (s StringList) Value() (driver.Value, error) {
	b, err := json.Marshal([]string(s))
	return string(b), err
}

func (s *StringList) Scan(value any) error {
	switch v := value.(type) {
	case string:
		return json.Unmarshal([]byte(v), s)
	case []byte:
		return json.Unmarshal(v, s)
	case nil:
		*s = nil
		return nil
	default:
		return fmt.Errorf("unsupported type for StringList: %T", value)
	}
}
