package domain

import "time"

type Group struct {
	Id          string
	Name        string
	Description string

	// Role is a site-wide role column from the original schema; it is not
	// used for authorization. Always RoleUser for now.
	Role Role

	// OwnerId is the group's single owner, kept in sync with the owner's
	// group_member row.
	OwnerId   string
	OwnerUser User `gorm:"foreignKey:OwnerId;references:Id"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (g *Group) TableName() string {
	return "group"
}

// GroupRole is a user's role inside a group. It does not grant anything on
// repositories by itself; the group's repository_member row does.
type GroupRole string

const (
	GroupRoleMember     GroupRole = "member"
	GroupRoleMaintainer GroupRole = "maintainer"
	GroupRoleOwner      GroupRole = "owner"
)

// Level orders group roles: member < maintainer < owner.
func (r GroupRole) Level() int {
	switch r {
	case GroupRoleMember:
		return 1
	case GroupRoleMaintainer:
		return 2
	case GroupRoleOwner:
		return 3
	default:
		return 0
	}
}

type GroupMember struct {
	Id      string
	GroupId string
	UserId  string
	User    User `gorm:"foreignKey:UserId;references:Id"`
	Role    GroupRole

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (GroupMember) TableName() string {
	return "group_member"
}

type GroupPers interface {
	// Create stores the group and its owner's membership in one transaction.
	Create(group *Group, owner *GroupMember) error
	GetById(id string) (*Group, error)
	GetByName(name string) (*Group, error)
	// Delete removes the group, its memberships and its repository_member rows.
	Delete(id string) error
	// ListForUser returns the groups userId is a member of.
	ListForUser(userId string) ([]Group, error)
	// GroupIdsForUser returns the IDs of the groups userId is a member of.
	GroupIdsForUser(userId string) ([]string, error)

	GetMember(groupId, userId string) (*GroupMember, error)
	ListMembers(groupId string) ([]GroupMember, error)
	AddMember(member *GroupMember) error
	UpdateMemberRole(groupId, userId string, role GroupRole) error
	RemoveMember(groupId, userId string) error
	// TransferOwnership makes newOwnerId the owner and demotes the current
	// owner to maintainer, in one transaction.
	TransferOwnership(groupId, currentOwnerId, newOwnerId string) error
}
