package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type releasePers struct{ db *gorm.DB }

func NewReleasePers(db *gorm.DB) *releasePers { return &releasePers{db: db} }

func (p *releasePers) Create(r *domain.Release) error {
	r.Id = utils.UUIDv4()
	return p.db.Omit("Author").Create(r).Error
}

func (p *releasePers) first(q *gorm.DB) (*domain.Release, error) {
	var r domain.Release
	if err := q.Preload("Author").First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *releasePers) Get(repositoryId, id string) (*domain.Release, error) {
	return p.first(p.db.Where("repository_id = ? AND id = ?", repositoryId, id))
}

func (p *releasePers) ByTag(repositoryId, tag string) (*domain.Release, error) {
	return p.first(p.db.Where("repository_id = ? AND tag_name = ?", repositoryId, tag))
}

func (p *releasePers) List(repositoryId string, drafts bool, offset, limit int) ([]domain.Release, error) {
	q := p.db.Preload("Author").Where("repository_id = ?", repositoryId)
	if !drafts {
		q = q.Where("draft = ?", false)
	}
	var out []domain.Release
	// Drafts (no publication date) first, then the newest published.
	err := q.Order("CASE WHEN published_at IS NULL THEN 0 ELSE 1 END, published_at DESC, created_at DESC").Offset(offset).Limit(limit).Find(&out).Error
	return out, err
}

func (p *releasePers) Latest(repositoryId string) (*domain.Release, error) {
	return p.first(p.db.Where("repository_id = ? AND draft = ? AND prerelease = ?", repositoryId, false, false).Order("published_at DESC"))
}

func (p *releasePers) Previous(repositoryId string, before time.Time) (*domain.Release, error) {
	return p.first(p.db.Where("repository_id = ? AND draft = ? AND published_at < ?", repositoryId, false, before).Order("published_at DESC"))
}

func (p *releasePers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.Release{}).Where("id = ?", id).Updates(fields).Error
}

func (p *releasePers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.Release{}).Error
}
