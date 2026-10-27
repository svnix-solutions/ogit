package persistence

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type mirrorPers struct{ db *gorm.DB }

func NewMirrorPers(db *gorm.DB) *mirrorPers { return &mirrorPers{db: db} }

func (p *mirrorPers) Create(m *domain.Mirror) error {
	m.Id = utils.UUIDv4()
	return p.db.Create(m).Error
}

func (p *mirrorPers) ForRepository(repositoryId string) (*domain.Mirror, error) {
	var m domain.Mirror
	if err := p.db.Where("repository_id = ?", repositoryId).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (p *mirrorPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Mirror{}).Where("id = ?", id).Updates(fields).Error
}

func (p *mirrorPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.Mirror{}).Error
}

func (p *mirrorPers) Claim(now, lockUntil time.Time) (*domain.Mirror, error) {
	for range 5 {
		var m domain.Mirror
		err := p.db.Where("next_sync_at <= ? AND (locked_until IS NULL OR locked_until < ?)", now, now).
			Order("next_sync_at").First(&m).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		// Conditional: another server may have claimed it meanwhile.
		res := p.db.Model(&domain.Mirror{}).Where("id = ? AND (locked_until IS NULL OR locked_until < ?)", m.Id, now).
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
