package persistence

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

type ssoPers struct{ db *gorm.DB }

func NewSSOPers(db *gorm.DB) *ssoPers { return &ssoPers{db: db} }

func (p *ssoPers) Providers() ([]domain.SSOProvider, error) {
	var out []domain.SSOProvider
	err := p.db.Order("name, slug").Find(&out).Error
	return out, err
}

func (p *ssoPers) Provider(slug string) (*domain.SSOProvider, error) {
	var out domain.SSOProvider
	if err := p.db.Where("slug = ?", slug).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *ssoPers) CreateProvider(pr *domain.SSOProvider) error {
	pr.Id = utils.UUIDv4()
	// Booleans are explicit: the columns' defaults would win over false.
	return p.db.Select("*").Create(pr).Error
}

func (p *ssoPers) UpdateProvider(id string, fields map[string]any) error {
	return p.db.Model(&domain.SSOProvider{}).Where("id = ?", id).Updates(fields).Error
}

func (p *ssoPers) DeleteProvider(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.SSOProvider{}).Error
}

func (p *ssoPers) Identity(provider, subject string) (*domain.UserIdentity, error) {
	var out domain.UserIdentity
	if err := p.db.Where("provider = ? AND subject = ?", provider, subject).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *ssoPers) Identities(userId string) ([]domain.UserIdentity, error) {
	var out []domain.UserIdentity
	err := p.db.Where("user_id = ?", userId).Order("created_at, id").Find(&out).Error
	return out, err
}

func (p *ssoPers) CreateIdentity(i *domain.UserIdentity) error {
	i.Id = utils.UUIDv4()
	return p.db.Create(i).Error
}

func (p *ssoPers) TouchIdentity(id string, at time.Time) error {
	return p.db.Model(&domain.UserIdentity{}).Where("id = ?", id).Update("last_login_at", at).Error
}

func (p *ssoPers) DeleteIdentity(userId, id string) error {
	res := p.db.Where("user_id = ? AND id = ?", userId, id).Delete(&domain.UserIdentity{})
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}

func (p *ssoPers) CreateFlow(f *domain.SSOFlow) error { return p.db.Create(f).Error }

// take deletes and returns the unexpired row id of T, once: the delete
// decides who gets it.
func take[T any](db *gorm.DB, id string, now time.Time) (*T, error) {
	var row T
	if err := db.Where("id = ? AND expires_at > ?", id, now).First(&row).Error; err != nil {
		return nil, err
	}
	res := db.Where("id = ?", id).Delete(new(T))
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return &row, nil
}

func (p *ssoPers) TakeFlow(id string, now time.Time) (*domain.SSOFlow, error) {
	return take[domain.SSOFlow](p.db, id, now)
}

func (p *ssoPers) Flow(id string, now time.Time) (*domain.SSOFlow, error) {
	var f domain.SSOFlow
	if err := p.db.Where("id = ? AND expires_at > ?", id, now).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (p *ssoPers) SetFlowResult(id, result string) error {
	return p.db.Model(&domain.SSOFlow{}).Where("id = ?", id).Update("result", result).Error
}

func (p *ssoPers) UseAssertion(id string, expires time.Time) (bool, error) {
	err := p.db.Exec("INSERT INTO sso_used_assertion (id, expires_at) VALUES (?, ?)", id, expires).Error
	if helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return false, nil
	}
	return err == nil, err
}

func (p *ssoPers) CreatePendingLink(l *domain.SSOPendingLink) error { return p.db.Create(l).Error }

func (p *ssoPers) TakePendingLink(id string, now time.Time) (*domain.SSOPendingLink, error) {
	return take[domain.SSOPendingLink](p.db, id, now)
}

func (p *ssoPers) PendingLink(id string, now time.Time) (*domain.SSOPendingLink, error) {
	var row domain.SSOPendingLink
	if err := p.db.Where("id = ? AND expires_at > ?", id, now).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

func (p *ssoPers) DeleteExpired(now time.Time) error {
	return errors.Join(
		p.db.Where("expires_at <= ?", now).Delete(&domain.SSOFlow{}).Error,
		p.db.Where("expires_at <= ?", now).Delete(&domain.SSOPendingLink{}).Error,
		p.db.Exec("DELETE FROM sso_used_assertion WHERE expires_at <= ?", now).Error,
	)
}
