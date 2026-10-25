package persistence

import (
	"crypto/rand"
	"encoding/binary"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type releasePers struct{ db *gorm.DB }

func NewReleasePers(db *gorm.DB) *releasePers { return &releasePers{db: db} }

func (p *releasePers) Create(r *domain.Release) error {
	r.Id, r.ApiId = utils.UUIDv4(), newApiId()
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

func (p *releasePers) ByApiId(repositoryId string, apiId int64) (*domain.Release, error) {
	return p.first(p.db.Where("repository_id = ? AND api_id = ?", repositoryId, apiId))
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

// Delete deletes the release and its asset rows (SQLite does not
// cascade); the caller deletes the files.
func (p *releasePers) Delete(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("release_id = ?", id).Delete(&domain.ReleaseAsset{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&domain.Release{}).Error
	})
}

// newApiId returns a random id in [1, 2^53).
func newApiId() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int64(binary.BigEndian.Uint64(b[:])&(1<<53-1)) | 1
}

type releaseAssetPers struct{ db *gorm.DB }

func NewReleaseAssetPers(db *gorm.DB) *releaseAssetPers { return &releaseAssetPers{db: db} }

func (p *releaseAssetPers) Create(a *domain.ReleaseAsset) error {
	if a.Id == "" {
		a.Id = utils.UUIDv4()
	}
	a.ApiId = newApiId()
	return p.db.Omit("Uploader").Create(a).Error
}

func (p *releaseAssetPers) first(q *gorm.DB) (*domain.ReleaseAsset, error) {
	var a domain.ReleaseAsset
	if err := q.Preload("Uploader").First(&a).Error; err != nil {
		return nil, err
	}
	return &a, nil
}

func (p *releaseAssetPers) Get(repositoryId, id string) (*domain.ReleaseAsset, error) {
	return p.first(p.db.Where("repository_id = ? AND id = ?", repositoryId, id))
}

func (p *releaseAssetPers) ByApiId(repositoryId string, apiId int64) (*domain.ReleaseAsset, error) {
	return p.first(p.db.Where("repository_id = ? AND api_id = ?", repositoryId, apiId))
}

func (p *releaseAssetPers) ByName(releaseId, name string) (*domain.ReleaseAsset, error) {
	return p.first(p.db.Where("release_id = ? AND name = ?", releaseId, name))
}

func (p *releaseAssetPers) List(releaseIds ...string) ([]domain.ReleaseAsset, error) {
	var out []domain.ReleaseAsset
	if len(releaseIds) == 0 {
		return out, nil
	}
	err := p.db.Preload("Uploader").Where("release_id IN ?", releaseIds).Order("name").Find(&out).Error
	return out, err
}

func (p *releaseAssetPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.ReleaseAsset{}).Where("id = ?", id).Updates(fields).Error
}

func (p *releaseAssetPers) CountDownload(id string) error {
	return p.db.Model(&domain.ReleaseAsset{}).Where("id = ?", id).UpdateColumn("download_count", gorm.Expr("download_count + 1")).Error
}

func (p *releaseAssetPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.ReleaseAsset{}).Error
}
