package persistence

import (
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type branchProtectionPers struct{ db *gorm.DB }

func NewBranchProtectionPers(db *gorm.DB) *branchProtectionPers { return &branchProtectionPers{db: db} }

func (p *branchProtectionPers) List(repositoryId string) ([]domain.BranchProtection, error) {
	var rules []domain.BranchProtection
	err := p.db.Where("repository_id = ?", repositoryId).Order("pattern").Find(&rules).Error
	return rules, err
}

func (p *branchProtectionPers) Get(repositoryId, id string) (*domain.BranchProtection, error) {
	var r domain.BranchProtection
	if err := p.db.Where("repository_id = ? AND id = ?", repositoryId, id).First(&r).Error; err != nil {
		return nil, err
	}
	return &r, nil
}

func (p *branchProtectionPers) Create(rule *domain.BranchProtection) error {
	rule.Id = utils.UUIDv4()
	if rule.AllowedUserIds == nil {
		rule.AllowedUserIds = domain.StringList{}
	}
	if rule.AllowedGroupIds == nil {
		rule.AllowedGroupIds = domain.StringList{}
	}
	return p.db.Create(rule).Error
}

func (p *branchProtectionPers) Update(id string, fields map[string]any) error {
	return p.db.Model(&domain.BranchProtection{}).Where("id = ?", id).Updates(fields).Error
}

func (p *branchProtectionPers) Delete(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.BranchProtection{}).Error
}
