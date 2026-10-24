package application

import (
	"errors"

	"github.com/labbs/ogit/domain"
)

// Errors returned by the membership and group operations. The HTTP layer
// maps them to status codes (see interfaces/http/v1/apierror).
var (
	// 404: also used for things the caller may not see, so that private
	// repositories and other people's groups are not disclosed.
	ErrGroupNotFound  = errors.New("group not found")
	ErrUserNotFound   = errors.New("user not found")
	ErrMemberNotFound = errors.New("member not found")

	// 403: the caller can see the resource but may not do this.
	ErrForbidden = errors.New("permission denied")

	// 400
	ErrInvalidRole      = errors.New("invalid role")
	ErrInvalidGroupName = errors.New("group name must be 1-64 characters: lowercase letters, digits, '.', '_' or '-', starting with a letter or digit, not ending in .git, and not a reserved word")
	ErrInvalidMember    = errors.New("specify exactly one of username or group")

	// 409
	ErrAlreadyMember     = errors.New("already a member")
	ErrGroupNameTaken    = errors.New("group name already taken (by a group or a user)")
	ErrLastOwner         = domain.ErrLastOwner
	ErrOwnerMustTransfer = errors.New("the group owner must transfer ownership first")
)
