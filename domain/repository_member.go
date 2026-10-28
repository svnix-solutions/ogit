package domain

import "time"

type RepositoryMember struct {
	Id           string
	RepositoryId string
	Repository   Repository `gorm:"foreignKey:RepositoryId;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	MemberType MemberType

	UserId *string
	User   User `gorm:"foreignKey:UserId;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	GroupId *string
	Group   Group `gorm:"foreignKey:GroupId;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;"`

	AccessType AccessType

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (RepositoryMember) TableName() string {
	return "repository_member"
}

type RepositoryMemberPers interface {
	// GetByRepositoryWithRelations returns all members with User and Group loaded.
	GetByRepositoryWithRelations(repositoryId string) ([]RepositoryMember, error)
	GetById(repositoryId, memberId string) (*RepositoryMember, error)
	GetUserMember(repositoryId, userId string) (*RepositoryMember, error)
	GetGroupMember(repositoryId, groupId string) (*RepositoryMember, error)
	Create(member *RepositoryMember) error
	// UpdateAccess changes a member's role. If the change removes an owner,
	// it fails with ErrLastOwner when no other owner would remain; the check
	// and the update run in one transaction.
	UpdateAccess(member *RepositoryMember, access AccessType) error
	// Delete removes a member, with the same last-owner protection.
	Delete(member *RepositoryMember) error
	// ListRepositoriesForMember returns the repositories where userId, or one
	// of groupIds, is a member, plus those owned by userId or one of groupIds.
	ListRepositoriesForMember(userId string, groupIds []string) ([]Repository, error)
}
