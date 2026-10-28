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
	var ids, keys []string
	if err := p.db.Model(&domain.UserTOTP{}).Where("user_id IN ? AND confirmed_at IS NOT NULL", userIds).Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}
	if err := p.db.Model(&domain.WebAuthnCredential{}).Where("user_id IN ?", userIds).Distinct().Pluck("user_id", &keys).Error; err != nil {
		return nil, err
	}
	for _, id := range append(ids, keys...) {
		out[id] = true
	}
	return out, nil
}

func (p *twoFactorPers) DeleteRecoveryCodes(userId string) error {
	return p.db.Where("user_id = ?", userId).Delete(&domain.RecoveryCode{}).Error
}

func (p *twoFactorPers) ListCredentials(userId string) ([]domain.WebAuthnCredential, error) {
	var out []domain.WebAuthnCredential
	err := p.db.Where("user_id = ?", userId).Order("created_at").Find(&out).Error
	return out, err
}

func (p *twoFactorPers) CredentialByCredentialId(credentialId string) (*domain.WebAuthnCredential, error) {
	var c domain.WebAuthnCredential
	if err := p.db.Where("credential_id = ?", credentialId).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (p *twoFactorPers) CreateCredential(c *domain.WebAuthnCredential) error {
	c.Id = utils.UUIDv4()
	return p.db.Create(c).Error
}

func (p *twoFactorPers) UpdateCredential(id, data string, usedAt time.Time) error {
	return p.db.Model(&domain.WebAuthnCredential{}).Where("id = ?", id).Updates(map[string]any{"data": data, "last_used_at": usedAt}).Error
}

func (p *twoFactorPers) DeleteCredential(userId, id string) (bool, error) {
	res := p.db.Where("user_id = ? AND id = ?", userId, id).Delete(&domain.WebAuthnCredential{})
	return res.RowsAffected == 1, res.Error
}

func (p *twoFactorPers) CreateCeremony(c *domain.WebAuthnCeremony) error {
	c.Id = utils.UUIDv4()
	return p.db.Create(c).Error
}

// TakeCeremony deletes the row it returns; a concurrent second take finds
// nothing to delete and fails.
func (p *twoFactorPers) TakeCeremony(id string) (*domain.WebAuthnCeremony, error) {
	var c domain.WebAuthnCeremony
	if err := p.db.Where("id = ?", id).First(&c).Error; err != nil {
		return nil, err
	}
	res := p.db.Where("id = ?", id).Delete(&domain.WebAuthnCeremony{})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	return &c, nil
}

func (p *twoFactorPers) DeleteExpiredCeremonies(now time.Time) (int64, error) {
	res := p.db.Where("expires_at < ?", now).Delete(&domain.WebAuthnCeremony{})
	return res.RowsAffected, res.Error
}

func (p *twoFactorPers) Disable(userId string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userId).Delete(&domain.RecoveryCode{}).Error; err != nil {
			return err
		}
		for _, model := range []any{&domain.LoginChallenge{}, &domain.WebAuthnCredential{}, &domain.WebAuthnCeremony{}} {
			if err := tx.Where("user_id = ?", userId).Delete(model).Error; err != nil {
				return err
			}
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
