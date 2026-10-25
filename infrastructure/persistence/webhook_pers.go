package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type webhookPers struct{ db *gorm.DB }

func NewWebhookPers(db *gorm.DB) *webhookPers { return &webhookPers{db: db} }

func (p *webhookPers) Create(h *domain.Webhook) error {
	h.Id = utils.UUIDv4()
	return p.db.Create(h).Error
}

// Get returns a repository's hook; with repositoryId "", any hook (the
// delivery worker).
func (p *webhookPers) Get(repositoryId, id string) (*domain.Webhook, error) {
	q := p.db.Where("id = ?", id)
	if repositoryId != "" {
		q = q.Where("repository_id = ?", repositoryId)
	}
	var h domain.Webhook
	if err := q.First(&h).Error; err != nil {
		return nil, err
	}
	return &h, nil
}

func (p *webhookPers) List(repositoryId string) ([]domain.Webhook, error) {
	var hooks []domain.Webhook
	err := p.db.Where("repository_id = ?", repositoryId).Order("created_at, id").Find(&hooks).Error
	return hooks, err
}

func (p *webhookPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Webhook{}).Where("id = ?", id).Updates(fields).Error
}

// Delete removes the hook and its deliveries (SQLite does not enforce the
// cascade here).
func (p *webhookPers) Delete(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("webhook_id = ?", id).Delete(&domain.WebhookDelivery{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&domain.Webhook{}).Error
	})
}

func (p *webhookPers) Enqueue(deliveries []domain.WebhookDelivery) error {
	if len(deliveries) == 0 {
		return nil
	}
	for i := range deliveries {
		deliveries[i].Id = utils.UUIDv4()
	}
	return p.db.Create(&deliveries).Error
}

// Claim locks each candidate with a conditional update: only one server's
// update matches a row, so a delivery is sent by one server at a time.
func (p *webhookPers) Claim(now, lockUntil time.Time, n int) ([]domain.WebhookDelivery, error) {
	var candidates []domain.WebhookDelivery
	err := p.db.Where("status = ? AND next_attempt_at <= ? AND (locked_until IS NULL OR locked_until < ?)", domain.DeliveryPending, now, now).
		Order("next_attempt_at").Limit(n).Find(&candidates).Error
	if err != nil {
		return nil, err
	}
	var claimed []domain.WebhookDelivery
	for _, d := range candidates {
		res := p.db.Model(&domain.WebhookDelivery{}).
			Where("id = ? AND status = ? AND (locked_until IS NULL OR locked_until < ?)", d.Id, domain.DeliveryPending, now).
			Update("locked_until", lockUntil)
		if res.Error != nil {
			return claimed, res.Error
		}
		if res.RowsAffected == 1 {
			d.LockedUntil = &lockUntil
			claimed = append(claimed, d)
		}
	}
	return claimed, nil
}

func (p *webhookPers) Finish(id string, fields map[string]any) error {
	fields["locked_until"] = nil
	return p.db.Model(&domain.WebhookDelivery{}).Where("id = ?", id).Updates(fields).Error
}

func (p *webhookPers) Deliveries(webhookId string, limit int) ([]domain.WebhookDelivery, error) {
	var ds []domain.WebhookDelivery
	err := p.db.Where("webhook_id = ?", webhookId).Order("created_at DESC, id DESC").Limit(limit).Find(&ds).Error
	return ds, err
}

func (p *webhookPers) Delivery(webhookId, id string) (*domain.WebhookDelivery, error) {
	var d domain.WebhookDelivery
	if err := p.db.Where("webhook_id = ? AND id = ?", webhookId, id).First(&d).Error; err != nil {
		return nil, err
	}
	return &d, nil
}

// Prune deletes delivered and failed deliveries beyond the newest keep of
// each hook. Pending ones are never pruned.
func (p *webhookPers) Prune(keep int) error {
	var hookIds []string
	if err := p.db.Model(&domain.Webhook{}).Pluck("id", &hookIds).Error; err != nil {
		return err
	}
	for _, id := range hookIds {
		var cutoff domain.WebhookDelivery
		err := p.db.Where("webhook_id = ?", id).Order("created_at DESC, id DESC").Offset(keep).Limit(1).Find(&cutoff).Error
		if err != nil {
			return err
		}
		if cutoff.Id == "" {
			continue
		}
		if err := p.db.Where("webhook_id = ? AND status <> ? AND created_at <= ?", id, domain.DeliveryPending, cutoff.CreatedAt).
			Delete(&domain.WebhookDelivery{}).Error; err != nil {
			return err
		}
	}
	return nil
}
