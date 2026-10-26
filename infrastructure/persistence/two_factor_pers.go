package persistence

import (
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type twoFactorPers struct{ db *gorm.DB }

func NewTwoFactorPers(db *gorm.DB) *twoFactorPers { return &twoFactorPers{db: db} }

func (p *twoFactorPers) GetTOTP(userId string) (*domain.UserTOTP, error) {
	var t domain.UserTOTP
	if err := p.db.Where("user_id = ?", userId).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (p *twoFactorPers) SaveTOTP(t *domain.UserTOTP) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", t.UserId).Delete(&domain.UserTOTP{}).Error; err != nil {
			return err
		}
		return tx.Create(t).Error
	})
}

func (p *twoFactorPers) ConfirmTOTP(userId string, step int64, at time.Time) error {
	return p.db.Model(&domain.UserTOTP{}).Where("user_id = ?", userId).Updates(map[string]any{"confirmed_at": at, "last_step": step}).Error
}

// UseStep is a conditional UPDATE, so two requests can't both use a step.
func (p *twoFactorPers) UseStep(userId string, step int64) (bool, error) {
	res := p.db.Model(&domain.UserTOTP{}).Where("user_id = ? AND last_step < ?", userId, step).Update("last_step", step)
	return res.RowsAffected == 1, res.Error
}

func (p *twoFactorPers) ReplaceRecoveryCodes(userId string, hashes []string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&domain.RecoveryCode{}).Error; err != nil {
			return err
		}
		for _, h := range hashes {
			if err := tx.Create(&domain.RecoveryCode{Id: utils.UUIDv4(), UserId: userId, CodeHash: h}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *twoFactorPers) UseRecoveryCode(userId, hash string, at time.Time) (bool, error) {
	res := p.db.Model(&domain.RecoveryCode{}).Where("user_id = ? AND code_hash = ? AND used_at IS NULL", userId, hash).Update("used_at", at)
	return res.RowsAffected == 1, res.Error
}

func (p *twoFactorPers) CountRecoveryCodes(userId string) (int64, error) {
	var n int64
	err := p.db.Model(&domain.RecoveryCode{}).Where("user_id = ? AND used_at IS NULL", userId).Count(&n).Error
	return n, err
}

func (p *twoFactorPers) EnabledUsers(userIds []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(userIds) == 0 {
		return out, nil
	}
	var ids []string
	err := p.db.Model(&domain.UserTOTP{}).Where("user_id IN ? AND confirmed_at IS NOT NULL", userIds).Pluck("user_id", &ids).Error
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

func (p *twoFactorPers) Disable(userId string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&domain.RecoveryCode{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userId).Delete(&domain.LoginChallenge{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", userId).Delete(&domain.UserTOTP{}).Error
	})
}

func (p *twoFactorPers) CreateChallenge(c *domain.LoginChallenge) error {
	c.Id = utils.UUIDv4()
	return p.db.Create(c).Error
}

func (p *twoFactorPers) GetChallenge(tokenHash string) (*domain.LoginChallenge, error) {
	var c domain.LoginChallenge
	if err := p.db.Where("token_hash = ?", tokenHash).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *twoFactorPers) FailChallenge(id string) (int, error) {
	if err := p.db.Model(&domain.LoginChallenge{}).Where("id = ?", id).UpdateColumn("attempts", gorm.Expr("attempts + 1")).Error; err != nil {
		return 0, err
	}
	var c domain.LoginChallenge
	err := p.db.Select("attempts").Where("id = ?", id).First(&c).Error
	return c.Attempts, err
}

func (p *twoFactorPers) DeleteChallenge(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.LoginChallenge{}).Error
}

func (p *twoFactorPers) DeleteExpiredChallenges(now time.Time) (int64, error) {
	res := p.db.Where("expires_at < ?", now).Delete(&domain.LoginChallenge{})
	return res.RowsAffected, res.Error
}
