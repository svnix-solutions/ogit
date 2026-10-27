package domain

import (
	"strings"
	"time"
)

type Group struct {
	Id string
	// Name is the group's full path: "org", or "org/team" for a sub-group
	// (see GroupPath).
	Name        string
	Description string
	// ParentId is the parent group of a sub-group (nil at the top level).
	ParentId *string

	// Role is a site-wide role column from the original schema; it is not
	// used for authorization. Always RoleUser for now.
	Role Role

	// OwnerId is the group's single owner, kept in sync with the owner's
	// group_member row.
	OwnerId string
	// RequireTwoFactor: members without two-factor authentication get no
	// access to the group's repositories through the group.
	RequireTwoFactor bool
	OwnerUser        User `gorm:"foreignKey:OwnerId;references:Id"`

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (g *Group) TableName() string {
	return "group"
}

// MaxGroupDepth is how deep groups nest: org/a/b/c/d.
const MaxGroupDepth = 5

// Segment is the last part of the group's path (its own name).
func (g *Group) Segment() string { return g.Name[strings.LastIndex(g.Name, "/")+1:] }

// GroupPathPrefixes lists a group path and its ancestors' paths, top
// first: "a/b/c" gives a, a/b, a/b/c.
func GroupPathPrefixes(path string) []string {
	parts := strings.Split(path, "/")
	out := make([]string, len(parts))
	for i := range parts {
		out[i] = strings.Join(parts[:i+1], "/")
	}
	return out
}

// ValidGroupPath checks a group path: a valid top-level name, then valid
// segments, at most MaxGroupDepth levels.
func ValidGroupPath(path string) error {
	parts := strings.Split(path, "/")
	if len(parts) > MaxGroupDepth {
		return ErrInvalidNamespaceName
	}
	if err := ValidNamespaceName(parts[0]); err != nil {
		return err
	}
	for _, p := range parts[1:] {
		if !namespacePattern.MatchString(p) || strings.HasSuffix(p, ".git") {
			return ErrInvalidNamespaceName
		}
	}
	return nil
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
	// GetByNames returns the groups with these paths (those that exist).
	GetByNames(names []string) ([]Group, error)
	GetByIds(ids []string) ([]Group, error)
	// Descendants returns every group below path (not path itself).
	Descendants(path string) ([]Group, error)
	// Children returns the sub-groups directly under a group.
	Children(parentId string) ([]Group, error)
	// MembershipsOf returns userId's direct memberships.
	MembershipsOf(userId string) ([]GroupMember, error)
	// MembershipsIn returns userId's direct memberships in groupIds.
	MembershipsIn(userId string, groupIds []string) ([]GroupMember, error)
	// MovePath gives a group (and everything below it) the path newPath
	// under parentId (nil: the top level), in one transaction: group
	// names, the namespace of their repositories and packages, and
	// redirects from every old path (groups and repositories).
	MovePath(groupId string, parentId *string, oldPath, newPath string) error
	// ResolveRedirect returns the group a former path now points to.
	ResolveRedirect(path string) (*Group, error)
	// RedirectPaths returns which of paths are former group paths.
	RedirectPaths(paths []string) ([]string, error)
	// Delete removes the group, its memberships and its repository_member rows.
	Delete(id string) error
	// ListForUser returns the groups userId is a member of.
	ListForUser(userId string) ([]Group, error)
	// OwnedBy returns the groups userId owns.
	OwnedBy(userId string) ([]Group, error)
	// GroupIdsForUser returns the IDs of the groups userId is a member of.
	GroupIdsForUser(userId string) ([]string, error)
	// RequiringTwoFactor returns those of groupIds that require 2FA.
	RequiringTwoFactor(groupIds []string) ([]string, error)
	SetRequireTwoFactor(groupId string, on bool) error

	GetMember(groupId, userId string) (*GroupMember, error)
	ListMembers(groupId string) ([]GroupMember, error)
	AddMember(member *GroupMember) error
	UpdateMemberRole(groupId, userId string, role GroupRole) error
	RemoveMember(groupId, userId string) error
	// TransferOwnership makes newOwnerId the owner and demotes the current
	// owner to maintainer, in one transaction.
	TransferOwnership(groupId, currentOwnerId, newOwnerId string) error
}

// GroupRedirect maps a former group path to the group (after a rename or
// a move).
type GroupRedirect struct {
	Path      string `gorm:"primaryKey"`
	GroupId   string
	CreatedAt time.Time
}

func (GroupRedirect) TableName() string { return "group_redirect" }
