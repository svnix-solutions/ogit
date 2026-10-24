package persistence

import (
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type repositoryPers struct {
	db *gorm.DB
}

func NewRepositoryPers(db *gorm.DB) *repositoryPers {
	return &repositoryPers{db: db}
}

func (r *repositoryPers) GetRepository(repo domain.Repository) (*domain.Repository, error) {
	var doc domain.Repository
	err := r.db.Where(repo).First(&doc).Error
	if err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *repositoryPers) Create(repo *domain.Repository) error {
	return r.db.Create(repo).Error
}

func (r *repositoryPers) Purge(id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		for _, q := range []struct {
			model any
			where string
		}{
			{&domain.RepositoryMember{}, "repository_id = ?"},
			{&domain.Token{}, "repository_id = ?"},
			{&domain.SSHKey{}, "repository_id = ?"},
			{&domain.Repository{}, "id = ?"},
		} {
			if err := tx.Where(q.where, id).Delete(q.model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *repositoryPers) List(limit, offset int) ([]domain.Repository, int64, error) {
	var total int64
	if err := r.db.Model(&domain.Repository{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var repos []domain.Repository
	err := r.db.Order("slug").Limit(limit).Offset(offset).Find(&repos).Error
	return repos, total, err
}

func (r *repositoryPers) SoleOwnedBy(userId string) ([]domain.Repository, error) {
	owned := r.db.Model(&domain.RepositoryMember{}).Select("repository_id").
		Where("member_type = ? AND user_id = ? AND access_type = ?", domain.MemberTypeUser, userId, domain.AccessTypeOwner)
	otherOwners := r.db.Model(&domain.RepositoryMember{}).Select("repository_id").
		Where("access_type = ? AND (user_id IS NULL OR user_id <> ?)", domain.AccessTypeOwner, userId)
	var repos []domain.Repository
	err := r.db.Where("id IN (?) AND id NOT IN (?)", owned, otherOwners).Order("slug").Find(&repos).Error
	return repos, err
}

func (r *repositoryPers) Delete(repo domain.Repository) error {
	return r.db.Where("id = ?", repo.Id).Delete(&domain.Repository{}).Error
}
