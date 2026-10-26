package persistence

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type lfsLockPers struct{ db *gorm.DB }

func NewLFSLockPers(db *gorm.DB) *lfsLockPers { return &lfsLockPers{db: db} }

func (p *lfsLockPers) Create(l *domain.LFSLock) error {
	l.Id = utils.UUIDv4()
	return p.db.Omit("Owner").Create(l).Error
}

func (p *lfsLockPers) first(q *gorm.DB) (*domain.LFSLock, error) {
	var l domain.LFSLock
	if err := q.Preload("Owner").First(&l).Error; err != nil {
		return nil, err
	}
	return &l, nil
}

func (p *lfsLockPers) Get(repositoryId, id string) (*domain.LFSLock, error) {
	return p.first(p.db.Where("repository_id = ? AND id = ?", repositoryId, id))
}

func (p *lfsLockPers) ByPath(repositoryId, path string) (*domain.LFSLock, error) {
	return p.first(p.db.Where("repository_id = ? AND path = ?", repositoryId, path))
}

func (p *lfsLockPers) List(repositoryId, path, id, cursor string, limit int) ([]domain.LFSLock, error) {
	q := p.db.Preload("Owner").Where("repository_id = ?", repositoryId)
	if path != "" {
		q = q.Where("path = ?", path)
	}
	if id != "" {
		q = q.Where("id = ?", id)
	}
	if cursor != "" {
		var c domain.LFSLock
		if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, cursor).First(&c).Error; err == nil {
			q = q.Where("path >= ?", c.Path)
		}
	}
	var out []domain.LFSLock
	err := q.Order("path").Limit(limit).Find(&out).Error
	return out, err
}

func (p *lfsLockPers) All(repositoryId string) ([]domain.LFSLock, error) {
	var out []domain.LFSLock
	err := p.db.Preload("Owner").Where("repository_id = ?", repositoryId).Order("path").Find(&out).Error
	return out, err
}

func (p *lfsLockPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.LFSLock{}).Error
}
