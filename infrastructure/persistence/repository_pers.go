package persistence

import (
	"strings"

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

func (r *repositoryPers) GetByPath(namespace, slug string) (*domain.Repository, error) {
	var doc domain.Repository
	if err := r.db.Where("namespace = ? AND slug = ?", namespace, slug).First(&doc).Error; err != nil {
		return nil, err
	}
	return &doc, nil
}

func (r *repositoryPers) InNamespace(ownerType domain.OwnerType, ownerId string) ([]domain.Repository, error) {
	var repos []domain.Repository
	err := r.db.Where("owner_type = ? AND owner_id = ?", ownerType, ownerId).Order("namespace, slug").Find(&repos).Error
	return repos, err
}

func (r *repositoryPers) Create(repo *domain.Repository) error {
	return r.db.Create(repo).Error
}

func (r *repositoryPers) Purge(id string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		// Pull requests' rows first: SQLite does not enforce the cascades.
		for _, table := range []string{"pull_request_label", "pull_request_comment", "pull_request_review"} {
			if err := tx.Exec("DELETE FROM "+table+" WHERE pull_request_id IN (SELECT id FROM pull_request WHERE repository_id = ?)", id).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("repository_id = ?", id).Delete(&domain.Label{}).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM webhook_delivery WHERE webhook_id IN (SELECT id FROM webhook WHERE repository_id = ?)", id).Error; err != nil {
			return err
		}
		if err := tx.Where("repository_id = ?", id).Delete(&domain.Webhook{}).Error; err != nil {
			return err
		}
		for _, q := range []struct {
			model any
			where string
		}{
			{&domain.RepositoryMember{}, "repository_id = ?"},
			{&domain.Token{}, "repository_id = ?"},
			{&domain.SSHKey{}, "repository_id = ?"},
			{&domain.RepositoryRedirect{}, "repository_id = ?"},
			{&domain.PullRequest{}, "repository_id = ?"},
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
	err := r.db.Order("namespace, slug").Limit(limit).Offset(offset).Find(&repos).Error
	return repos, total, err
}

func (r *repositoryPers) SoleOwnedBy(userId string) ([]domain.Repository, error) {
	owned := r.db.Model(&domain.RepositoryMember{}).Select("repository_id").
		Where("member_type = ? AND user_id = ? AND access_type = ?", domain.MemberTypeUser, userId, domain.AccessTypeOwner)
	otherOwners := r.db.Model(&domain.RepositoryMember{}).Select("repository_id").
		Where("access_type = ? AND (user_id IS NULL OR user_id <> ?)", domain.AccessTypeOwner, userId)
	var repos []domain.Repository
	err := r.db.Where("id IN (?) AND id NOT IN (?)", owned, otherOwners).Order("namespace, slug").Find(&repos).Error
	return repos, err
}

func (r *repositoryPers) Rename(id, namespace, name, newSlug, oldSlug string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.Repository{}).Where("id = ?", id).Updates(map[string]any{"name": name, "slug": newSlug}).Error; err != nil {
			return err
		}
		// The new path belongs to this repository now, including if it was
		// one of its own former paths.
		if err := tx.Where("namespace = ? AND slug = ?", namespace, newSlug).Delete(&domain.RepositoryRedirect{}).Error; err != nil {
			return err
		}
		if oldSlug == newSlug {
			return nil
		}
		return tx.Create(&domain.RepositoryRedirect{Namespace: namespace, Slug: oldSlug, RepositoryId: id}).Error
	})
}

func (r *repositoryPers) Move(id, oldNamespace, slug string, to domain.Namespace) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.Repository{}).Where("id = ?", id).
			Updates(map[string]any{"namespace": to.Path, "owner_type": to.Type, "owner_id": to.Id}).Error; err != nil {
			return err
		}
		if err := tx.Where("namespace = ? AND slug = ?", to.Path, slug).Delete(&domain.RepositoryRedirect{}).Error; err != nil {
			return err
		}
		return tx.Create(&domain.RepositoryRedirect{Namespace: oldNamespace, Slug: slug, RepositoryId: id}).Error
	})
}

func (r *repositoryPers) Update(id string, fields map[string]any) error {
	return r.db.Model(&domain.Repository{}).Where("id = ?", id).Updates(fields).Error
}

func (r *repositoryPers) DropRedirect(namespace, slug string) error {
	return r.db.Where("namespace = ? AND slug = ?", namespace, slug).Delete(&domain.RepositoryRedirect{}).Error
}

func (r *repositoryPers) ResolveRedirect(namespace, slug string) (*domain.Repository, error) {
	var redirect domain.RepositoryRedirect
	if err := r.db.Where("namespace = ? AND slug = ?", namespace, slug).First(&redirect).Error; err != nil {
		return nil, err
	}
	var repo domain.Repository
	err := r.db.Where("id = ?", redirect.RepositoryId).First(&repo).Error
	return &repo, err
}

func (r *repositoryPers) ListPublic(query string, limit, offset int) ([]domain.Repository, int64, error) {
	q := r.db.Model(&domain.Repository{}).Where("is_private = ?", false)
	if query != "" {
		like := "%" + strings.ToLower(query) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var repos []domain.Repository
	err := q.Order("namespace, slug").Limit(limit).Offset(offset).Find(&repos).Error
	return repos, total, err
}

func (r *repositoryPers) Delete(repo domain.Repository) error {
	return r.db.Where("id = ?", repo.Id).Delete(&domain.Repository{}).Error
}
