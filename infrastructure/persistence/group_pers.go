package persistence

import (
	"strings"

	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
)

type groupPers struct {
	db *gorm.DB
}

func NewGroupPers(db *gorm.DB) *groupPers {
	return &groupPers{db: db}
}

func (g *groupPers) Create(group *domain.Group, owner *domain.GroupMember) error {
	return g.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(group).Error; err != nil {
			return err
		}
		return tx.Create(owner).Error
	})
}

func (g *groupPers) GetById(id string) (*domain.Group, error) {
	var group domain.Group
	err := g.db.Where("id = ?", id).First(&group).Error
	return &group, err
}

func (g *groupPers) GetByName(name string) (*domain.Group, error) {
	var group domain.Group
	err := g.db.Where("name = ?", name).First(&group).Error
	return &group, err
}

func (g *groupPers) GetByNames(names []string) ([]domain.Group, error) {
	var groups []domain.Group
	if len(names) == 0 {
		return groups, nil
	}
	err := g.db.Where("name IN ?", names).Order("name").Find(&groups).Error
	return groups, err
}

func (g *groupPers) GetByIds(ids []string) ([]domain.Group, error) {
	var groups []domain.Group
	if len(ids) == 0 {
		return groups, nil
	}
	err := g.db.Where("id IN ?", ids).Order("name").Find(&groups).Error
	return groups, err
}

// likePrefix escapes a path for LIKE (names may contain '_').
func likePrefix(path string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(path) + "/%"
}

func (g *groupPers) Descendants(path string) ([]domain.Group, error) {
	var groups []domain.Group
	err := g.db.Where(`name LIKE ? ESCAPE '\'`, likePrefix(path)).Order("name").Find(&groups).Error
	return groups, err
}

func (g *groupPers) Children(parentId string) ([]domain.Group, error) {
	var groups []domain.Group
	err := g.db.Where("parent_id = ?", parentId).Order("name").Find(&groups).Error
	return groups, err
}

func (g *groupPers) MembershipsOf(userId string) ([]domain.GroupMember, error) {
	var members []domain.GroupMember
	err := g.db.Where("user_id = ?", userId).Find(&members).Error
	return members, err
}

func (g *groupPers) MembershipsIn(userId string, groupIds []string) ([]domain.GroupMember, error) {
	var members []domain.GroupMember
	if len(groupIds) == 0 {
		return members, nil
	}
	err := g.db.Where("user_id = ? AND group_id IN ?", userId, groupIds).Find(&members).Error
	return members, err
}

// Delete removes dependent rows explicitly: sqlite does not enforce the
// foreign key cascades.
func (g *groupPers) Delete(id string) error {
	return g.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("member_type = ? AND group_id = ?", domain.MemberTypeGroup, id).Delete(&domain.RepositoryMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&domain.GroupMember{}).Error; err != nil {
			return err
		}
		for _, model := range []any{&domain.ActionRunner{}, &domain.ActionRegistrationToken{}, &domain.ActionSecret{}, &domain.ActionVariable{}} {
			if err := tx.Where("scope_type = ? AND scope_id = ?", domain.RunnerScopeGroup, id).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Where("id = ?", id).Delete(&domain.Group{}).Error
	})
}

func (g *groupPers) ListForUser(userId string) ([]domain.Group, error) {
	var groups []domain.Group
	err := g.db.Where("id IN (?)", g.db.Model(&domain.GroupMember{}).Select("group_id").Where("user_id = ?", userId)).
		Order("name").Find(&groups).Error
	return groups, err
}

func (g *groupPers) OwnedBy(userId string) ([]domain.Group, error) {
	var groups []domain.Group
	err := g.db.Where("owner_id = ?", userId).Order("name").Find(&groups).Error
	return groups, err
}

func (g *groupPers) GroupIdsForUser(userId string) ([]string, error) {
	var ids []string
	err := g.db.Model(&domain.GroupMember{}).Where("user_id = ?", userId).Pluck("group_id", &ids).Error
	return ids, err
}

func (g *groupPers) GetMember(groupId, userId string) (*domain.GroupMember, error) {
	var member domain.GroupMember
	err := g.db.Preload("User").Where("group_id = ? AND user_id = ?", groupId, userId).First(&member).Error
	return &member, err
}

func (g *groupPers) ListMembers(groupId string) ([]domain.GroupMember, error) {
	var members []domain.GroupMember
	err := g.db.Preload("User").Where("group_id = ?", groupId).Order("created_at").Find(&members).Error
	return members, err
}

func (g *groupPers) AddMember(member *domain.GroupMember) error {
	return g.db.Create(member).Error
}

func (g *groupPers) UpdateMemberRole(groupId, userId string, role domain.GroupRole) error {
	res := g.db.Model(&domain.GroupMember{}).Where("group_id = ? AND user_id = ?", groupId, userId).Update("role", role)
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}

func (g *groupPers) RemoveMember(groupId, userId string) error {
	res := g.db.Where("group_id = ? AND user_id = ?", groupId, userId).Delete(&domain.GroupMember{})
	if res.Error == nil && res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return res.Error
}

func (g *groupPers) TransferOwnership(groupId, currentOwnerId, newOwnerId string) error {
	return g.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&domain.GroupMember{}).Where("group_id = ? AND user_id = ?", groupId, newOwnerId).
			Update("role", domain.GroupRoleOwner).Error; err != nil {
			return err
		}
		if err := tx.Model(&domain.GroupMember{}).Where("group_id = ? AND user_id = ?", groupId, currentOwnerId).
			Update("role", domain.GroupRoleMaintainer).Error; err != nil {
			return err
		}
		return tx.Model(&domain.Group{}).Where("id = ?", groupId).Update("owner_id", newOwnerId).Error
	})
}

func (g *groupPers) RequiringTwoFactor(groupIds []string) ([]string, error) {
	var ids []string
	if len(groupIds) == 0 {
		return ids, nil
	}
	err := g.db.Model(&domain.Group{}).Where("id IN ? AND require_two_factor = ?", groupIds, true).Pluck("id", &ids).Error
	return ids, err
}

func (g *groupPers) SetRequireTwoFactor(groupId string, on bool) error {
	return g.db.Model(&domain.Group{}).Where("id = ?", groupId).Update("require_two_factor", on).Error
}
