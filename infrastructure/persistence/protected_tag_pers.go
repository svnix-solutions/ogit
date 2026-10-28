package persistence

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type protectedTagPers struct{ db *gorm.DB }

func NewProtectedTagPers(db *gorm.DB) *protectedTagPers { return &protectedTagPers{db: db} }

func (p *protectedTagPers) List(repositoryId string) ([]domain.ProtectedTag, error) {
	var rules []domain.ProtectedTag
	err := p.db.Where("repository_id = ?", repositoryId).Order("pattern").Find(&rules).Error
	return rules, err
}

func (p *protectedTagPers) Get(repositoryId, id string) (*domain.ProtectedTag, error) {
	var r domain.ProtectedTag
	if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, id).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *protectedTagPers) Create(rule *domain.ProtectedTag) error {
	rule.Id = utils.UUIDv4()
	if rule.AllowedUserIds == nil {
		rule.AllowedUserIds = domain.StringList{}
	}
	if rule.AllowedGroupIds == nil {
		rule.AllowedGroupIds = domain.StringList{}
	}
	return p.db.Create(rule).Error
}

func (p *protectedTagPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.ProtectedTag{}).Where("id = ?", id).Updates(fields).Error
}

func (p *protectedTagPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.ProtectedTag{}).Error
}
