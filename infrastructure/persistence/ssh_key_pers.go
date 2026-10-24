package persistence

import (
	"time"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type sshKeyPers struct {
	db *gorm.DB
}

func NewSSHKeyPers(db *gorm.DB) *sshKeyPers {
	return &sshKeyPers{db: db}
}

func (p *sshKeyPers) Create(key *domain.SSHKey) error {
	return p.db.Create(key).Error
}

func (p *sshKeyPers) GetByFingerprint(fingerprint string) (*domain.SSHKey, error) {
	var key domain.SSHKey
	err := p.db.Preload("User").Preload("Repository").Where("fingerprint = ?", fingerprint).First(&key).Error
	return &key, err
}

func (p *sshKeyPers) ListForUser(userId string) ([]domain.SSHKey, error) {
	var keys []domain.SSHKey
	err := p.db.Where("user_id = ?", userId).Order("created_at").Find(&keys).Error
	return keys, err
}

func (p *sshKeyPers) ListForRepository(repositoryId string) ([]domain.SSHKey, error) {
	var keys []domain.SSHKey
	err := p.db.Where("repository_id = ?", repositoryId).Order("created_at").Find(&keys).Error
	return keys, err
}

func (p *sshKeyPers) DeleteForUser(userId, keyId string) error {
	return deleteOne(p.db.Where("user_id = ? AND id = ?", userId, keyId))
}

func (p *sshKeyPers) DeleteForRepository(repositoryId, keyId string) error {
	return deleteOne(p.db.Where("repository_id = ? AND id = ?", repositoryId, keyId))
}

func (p *sshKeyPers) TouchLastUsed(keyId string, at time.Time) error {
	return p.db.Model(&domain.SSHKey{}).Where("id = ?", keyId).Update("last_used_at", at).Error
}

func deleteOne(q *gorm.DB) error {
	res := q.Delete(&domain.SSHKey{})
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}
