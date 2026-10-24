package persistence

import (
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type emailVerificationPers struct {
	db *gorm.DB
}

func NewEmailVerificationPers(db *gorm.DB) *emailVerificationPers {
	return &emailVerificationPers{db: db}
}

func (p *emailVerificationPers) Replace(v *domain.EmailVerification) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", v.UserId).Delete(&domain.EmailVerification{}).Error; err != nil {
			return err
		}
		return tx.Create(v).Error
	})
}

func (p *emailVerificationPers) GetByTokenHash(hash string) (*domain.EmailVerification, error) {
	var v domain.EmailVerification
	err := p.db.Where("token_hash = ?", hash).First(&v).Error
	return &v, err
}

func (p *emailVerificationPers) GetByUserId(userId string) (*domain.EmailVerification, error) {
	var v domain.EmailVerification
	err := p.db.Where("user_id = ?", userId).First(&v).Error
	return &v, err
}

func (p *emailVerificationPers) DeleteByUserId(userId string) error {
	return p.db.Where("user_id = ?", userId).Delete(&domain.EmailVerification{}).Error
}
