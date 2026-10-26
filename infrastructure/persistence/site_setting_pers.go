package persistence

import (
	"time"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type siteSettingPers struct{ db *gorm.DB }

func NewSiteSettingPers(db *gorm.DB) *siteSettingPers { return &siteSettingPers{db: db} }

func (p *siteSettingPers) Get(key string) (string, error) {
	var s domain.SiteSetting
	err := p.db.Where("key = ?", key).First(&s).Error
	if err == gorm.ErrRecordNotFound {
		return "", nil
	}
	return s.Value, err
}

func (p *siteSettingPers) Set(key, value string) error {
	return p.db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).
		Create(&domain.SiteSetting{Key: key, Value: value, UpdatedAt: time.Now()}).Error
}
