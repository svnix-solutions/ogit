package persistence

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type pushMirrorPers struct{ db *gorm.DB }

func NewPushMirrorPers(db *gorm.DB) *pushMirrorPers { return &pushMirrorPers{db: db} }

func (p *pushMirrorPers) Create(m *domain.PushMirror) error {
	m.Id = utils.UUIDv4()
	return p.db.Create(m).Error
}

func (p *pushMirrorPers) Get(repositoryId, id string) (*domain.PushMirror, error) {
	var m domain.PushMirror
	if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, id).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (p *pushMirrorPers) List(repositoryId string) ([]domain.PushMirror, error) {
	var out []domain.PushMirror
	err := p.db.Where("repository_id = ?", repositoryId).Order("created_at, id").Find(&out).Error
	return out, err
}

func (p *pushMirrorPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.PushMirror{}).Where("id = ?", id).Updates(fields).Error
}

func (p *pushMirrorPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.PushMirror{}).Error
}

func (p *pushMirrorPers) Claim(now, lockUntil time.Time) (*domain.PushMirror, error) {
	for range 5 {
		var m domain.PushMirror
		err := p.db.Where("next_sync_at <= ? AND (locked_until IS NULL OR locked_until < ?)", now, now).
			Order("next_sync_at").First(&m).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		// Conditional: another server may have claimed it meanwhile.
		res := p.db.Model(&domain.PushMirror{}).Where("id = ? AND (locked_until IS NULL OR locked_until < ?)", m.Id, now).
			Updates(map[string]any{"locked_until": lockUntil, "status": domain.MirrorSyncing})
		if res.Error != nil {
			return nil, res.Error
		}
		if res.RowsAffected == 1 {
			m.LockedUntil, m.Status = &lockUntil, domain.MirrorSyncing
			return &m, nil
		}
	}
	return nil, nil
}

func (p *pushMirrorPers) Touch(repositoryId string, now time.Time) error {
	return p.db.Model(&domain.PushMirror{}).Where("repository_id = ?", repositoryId).Updates(map[string]any{
		"generation":   gorm.Expr("generation + 1"),
		"next_sync_at": gorm.Expr("CASE WHEN next_sync_at > ? THEN ? ELSE next_sync_at END", now, now),
	}).Error
}

func (p *pushMirrorPers) Schedule(id string, generation int, next time.Time) error {
	return p.db.Model(&domain.PushMirror{}).Where("id = ? AND generation = ?", id, generation).
		Update("next_sync_at", next).Error
}
