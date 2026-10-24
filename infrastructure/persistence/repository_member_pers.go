package persistence

import (
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type repositoryMemberPers struct {
	db *gorm.DB
}

func NewRepositoryMemberPers(db *gorm.DB) *repositoryMemberPers {
	return &repositoryMemberPers{db: db}
}

func (r *repositoryMemberPers) GetByRepositoryWithRelations(repositoryId string) ([]domain.RepositoryMember, error) {
	var members []domain.RepositoryMember
	err := r.db.Preload("User").Preload("Group").Where("repository_id = ?", repositoryId).Order("created_at").Find(&members).Error
	if err != nil {
		return nil, err
	}
	return members, nil
}

func (r *repositoryMemberPers) GetById(repositoryId, memberId string) (*domain.RepositoryMember, error) {
	var member domain.RepositoryMember
	err := r.db.Preload("User").Preload("Group").Where("repository_id = ? AND id = ?", repositoryId, memberId).First(&member).Error
	return &member, err
}

func (r *repositoryMemberPers) GetUserMember(repositoryId, userId string) (*domain.RepositoryMember, error) {
	var member domain.RepositoryMember
	err := r.db.Where("repository_id = ? AND member_type = ? AND user_id = ?", repositoryId, domain.MemberTypeUser, userId).First(&member).Error
	return &member, err
}

func (r *repositoryMemberPers) GetGroupMember(repositoryId, groupId string) (*domain.RepositoryMember, error) {
	var member domain.RepositoryMember
	err := r.db.Where("repository_id = ? AND member_type = ? AND group_id = ?", repositoryId, domain.MemberTypeGroup, groupId).First(&member).Error
	return &member, err
}

func (r *repositoryMemberPers) Create(member *domain.RepositoryMember) error {
	return r.db.Create(member).Error
}

func (r *repositoryMemberPers) UpdateAccess(member *domain.RepositoryMember, access domain.AccessType) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if member.AccessType == domain.AccessTypeOwner && access != domain.AccessTypeOwner {
			if err := ensureAnotherOwner(tx, member); err != nil {
				return err
			}
		}
		return tx.Model(&domain.RepositoryMember{}).Where("id = ?", member.Id).Update("access_type", access).Error
	})
}

func (r *repositoryMemberPers) Delete(member *domain.RepositoryMember) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if member.AccessType == domain.AccessTypeOwner {
			if err := ensureAnotherOwner(tx, member); err != nil {
				return err
			}
		}
		return tx.Where("id = ?", member.Id).Delete(&domain.RepositoryMember{}).Error
	})
}

// ensureAnotherOwner fails with domain.ErrLastOwner unless the repository has
// an owner besides member. On postgres the owner rows are locked so that two
// owners cannot remove each other concurrently; sqlite serializes write
// transactions and does not support FOR UPDATE.
func ensureAnotherOwner(tx *gorm.DB, member *domain.RepositoryMember) error {
	q := tx.Model(&domain.RepositoryMember{}).
		Where("repository_id = ? AND access_type = ? AND id <> ?", member.RepositoryId, domain.AccessTypeOwner, member.Id)
	if tx.Dialector.Name() == "postgres" {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var others []string
	if err := q.Pluck("id", &others).Error; err != nil {
		return err
	}
	if len(others) == 0 {
		return domain.ErrLastOwner
	}
	return nil
}

func (r *repositoryMemberPers) ListRepositoriesForMember(userId string, groupIds []string) ([]domain.Repository, error) {
	members := r.db.Model(&domain.RepositoryMember{}).Select("repository_id")
	if len(groupIds) > 0 {
		members = members.Where("(member_type = ? AND user_id = ?) OR (member_type = ? AND group_id IN ?)",
			domain.MemberTypeUser, userId, domain.MemberTypeGroup, groupIds)
	} else {
		members = members.Where("member_type = ? AND user_id = ?", domain.MemberTypeUser, userId)
	}

	// Plus everything in the user's namespace and their groups' namespaces.
	q := r.db.Where("id IN (?)", members).Or("owner_type = ? AND owner_id = ?", domain.OwnerUser, userId)
	if len(groupIds) > 0 {
		q = q.Or("owner_type = ? AND owner_id IN ?", domain.OwnerGroup, groupIds)
	}
	var repos []domain.Repository
	err := q.Order("namespace, slug").Find(&repos).Error
	return repos, err
}
