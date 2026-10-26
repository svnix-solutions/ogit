// Package apierror maps application errors to API error responses.
package apierror

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitmerge"
	"github.com/rs/zerolog"
)

var mapping = []struct {
	err  error
	code int
	typ  string
}{
	{application.ErrRepositoryNotFound, fiber.StatusNotFound, "REPOSITORY_NOT_FOUND"},
	{application.ErrGroupNotFound, fiber.StatusNotFound, "GROUP_NOT_FOUND"},
	{application.ErrUserNotFound, fiber.StatusNotFound, "USER_NOT_FOUND"},
	{application.ErrMemberNotFound, fiber.StatusNotFound, "MEMBER_NOT_FOUND"},
	{application.ErrForbidden, fiber.StatusForbidden, "FORBIDDEN"},
	{application.ErrInvalidRole, fiber.StatusBadRequest, "INVALID_ROLE"},
	{application.ErrInvalidGroupName, fiber.StatusBadRequest, "INVALID_GROUP_NAME"},
	{application.ErrInvalidMember, fiber.StatusBadRequest, "INVALID_MEMBER"},
	{application.ErrAlreadyMember, fiber.StatusConflict, "ALREADY_MEMBER"},
	{application.ErrGroupNameTaken, fiber.StatusConflict, "GROUP_NAME_TAKEN"},
	{application.ErrLastOwner, fiber.StatusConflict, "LAST_OWNER"},
	{application.ErrOwnerMustTransfer, fiber.StatusConflict, "OWNER_MUST_TRANSFER"},
	{application.ErrLastAdmin, fiber.StatusConflict, "LAST_ADMIN"},
	{application.ErrWouldOrphan, fiber.StatusConflict, "WOULD_ORPHAN"},
	{application.ErrAccountTaken, fiber.StatusConflict, "ACCOUNT_TAKEN"},
	{application.ErrConfirmMismatch, fiber.StatusBadRequest, "CONFIRM_MISMATCH"},
	{application.ErrRegistrationRejected, fiber.StatusBadRequest, "PASSWORD_REJECTED"},
	{application.ErrInvalidCredentials, fiber.StatusBadRequest, "INVALID_PASSWORD"},
	{application.ErrRepositoryNameTaken, fiber.StatusConflict, "REPOSITORY_NAME_TAKEN"},
	{application.ErrNamespaceNotFound, fiber.StatusNotFound, "NAMESPACE_NOT_FOUND"},
	{application.ErrTokenNotFound, fiber.StatusNotFound, "TOKEN_NOT_FOUND"},
	{application.ErrOwnReview, fiber.StatusConflict, "OWN_PULL_REQUEST"},
	{application.ErrEmptyComment, fiber.StatusBadRequest, "EMPTY_COMMENT"},
	{application.ErrInvalidLine, fiber.StatusBadRequest, "INVALID_LINE"},
	{application.ErrCommentNotFound, fiber.StatusNotFound, "COMMENT_NOT_FOUND"},
	{application.ErrInvalidReview, fiber.StatusBadRequest, "INVALID_REVIEW_STATE"},
	{application.ErrNeedsApproval, fiber.StatusConflict, "NEEDS_APPROVAL"},
	{application.ErrChangesRequested, fiber.StatusConflict, "CHANGES_REQUESTED"},
	{application.ErrLabelNotFound, fiber.StatusNotFound, "LABEL_NOT_FOUND"},
	{application.ErrInvalidLabel, fiber.StatusBadRequest, "INVALID_LABEL"},
	{application.ErrLabelExists, fiber.StatusConflict, "LABEL_EXISTS"},
	{application.ErrInvalidApprovals, fiber.StatusBadRequest, "INVALID_REQUIRED_APPROVALS"},
	{application.ErrInvalidWebhook, fiber.StatusBadRequest, "INVALID_WEBHOOK"},
	{application.ErrWebhookNotFound, fiber.StatusNotFound, "WEBHOOK_NOT_FOUND"},
	{application.ErrDeliveryNotFound, fiber.StatusNotFound, "DELIVERY_NOT_FOUND"},
	{application.ErrProtectedBranch, fiber.StatusForbidden, "BRANCH_PROTECTED"},
	{application.ErrRuleNotFound, fiber.StatusNotFound, "RULE_NOT_FOUND"},
	{application.ErrRuleExists, fiber.StatusConflict, "RULE_EXISTS"},
	{application.ErrInvalidRule, fiber.StatusBadRequest, "INVALID_RULE"},
	{application.ErrUnknownGroup, fiber.StatusBadRequest, "UNKNOWN_GROUP"},
	{application.ErrUnknownUser, fiber.StatusBadRequest, "UNKNOWN_USER"},
	{application.ErrInvalidStatus, fiber.StatusBadRequest, "INVALID_STATUS"},
	{application.ErrCommitNotFound, fiber.StatusUnprocessableEntity, "COMMIT_NOT_FOUND"},
	{application.ErrTokenScope, fiber.StatusForbidden, "TOKEN_SCOPE"},
	{application.ErrTooManyCommits, fiber.StatusBadRequest, "TOO_MANY_COMMITS"},
	{application.ErrChecksRequired, fiber.StatusConflict, "CHECKS_REQUIRED"},
	{application.ErrInvalidToken, fiber.StatusUnauthorized, "INVALID_TOKEN"},
	{application.ErrIssueNotFound, fiber.StatusNotFound, "ISSUE_NOT_FOUND"},
	{application.ErrNotAFork, fiber.StatusConflict, "NOT_A_FORK"},
	{application.ErrSudoRequired, fiber.StatusForbidden, "SUDO_REQUIRED"},
	{application.ErrInvalidCode, fiber.StatusUnauthorized, "INVALID_CODE"},
	{application.ErrChallengeExpired, fiber.StatusUnauthorized, "CHALLENGE_EXPIRED"},
	{application.ErrTwoFactorEnabled, fiber.StatusConflict, "TWO_FACTOR_ENABLED"},
	{application.ErrTwoFactorDisabled, fiber.StatusConflict, "TWO_FACTOR_DISABLED"},
	{application.ErrTwoFactorUnavailable, fiber.StatusConflict, "TWO_FACTOR_UNAVAILABLE"},
	{application.ErrNoPendingSetup, fiber.StatusConflict, "NO_PENDING_SETUP"},
	{application.ErrAccountDisabled, fiber.StatusForbidden, "ACCOUNT_DISABLED"},
	{application.ErrReleaseNotFound, fiber.StatusNotFound, "RELEASE_NOT_FOUND"},
	{application.ErrReleaseExists, fiber.StatusConflict, "RELEASE_EXISTS"},
	{application.ErrInvalidRelease, fiber.StatusBadRequest, "INVALID_RELEASE"},
	{application.ErrTagNotFound, fiber.StatusNotFound, "TAG_NOT_FOUND"},
	{application.ErrAssetNotFound, fiber.StatusNotFound, "ASSET_NOT_FOUND"},
	{application.ErrAssetExists, fiber.StatusUnprocessableEntity, "ASSET_EXISTS"},
	{application.ErrAssetTooLarge, fiber.StatusRequestEntityTooLarge, "ASSET_TOO_LARGE"},
	{application.ErrInvalidAsset, fiber.StatusBadRequest, "INVALID_ASSET"},
	{application.ErrProtectedTag, fiber.StatusForbidden, "TAG_PROTECTED"},
	{application.ErrTagRuleNotFound, fiber.StatusNotFound, "RULE_NOT_FOUND"},
	{application.ErrTagRuleExists, fiber.StatusConflict, "RULE_EXISTS"},
	{application.ErrInvalidTagRule, fiber.StatusBadRequest, "INVALID_RULE"},
	{application.ErrInvalidTokenRequest, fiber.StatusBadRequest, "INVALID_TOKEN_REQUEST"},
	{application.ErrTokenNameTaken, fiber.StatusConflict, "TOKEN_NAME_TAKEN"},
	{application.ErrDiverged, fiber.StatusConflict, "DIVERGED"},
	{application.ErrNotInNetwork, fiber.StatusBadRequest, "NOT_IN_NETWORK"},
	{application.ErrMilestoneNotFound, fiber.StatusNotFound, "MILESTONE_NOT_FOUND"},
	{application.ErrMilestoneExists, fiber.StatusConflict, "MILESTONE_EXISTS"},
	{application.ErrInvalidMilestone, fiber.StatusBadRequest, "INVALID_MILESTONE"},
	{application.ErrInvalidAssignee, fiber.StatusBadRequest, "INVALID_ASSIGNEE"},
	{application.ErrIssuesDisabled, fiber.StatusConflict, "ISSUES_DISABLED"},
	{application.ErrNumberNotFound, fiber.StatusNotFound, "NUMBER_NOT_FOUND"},
	{application.ErrInvalidState, fiber.StatusBadRequest, "INVALID_STATE"},
	{application.ErrPullRequestNotFound, fiber.StatusNotFound, "PULL_REQUEST_NOT_FOUND"},
	{application.ErrBranchNotFound, fiber.StatusNotFound, "BRANCH_NOT_FOUND"},
	{application.ErrSameBranch, fiber.StatusBadRequest, "SAME_BRANCH"},
	{application.ErrEmptyTitle, fiber.StatusBadRequest, "TITLE_REQUIRED"},
	{application.ErrPullRequestExists, fiber.StatusConflict, "PULL_REQUEST_EXISTS"},
	{application.ErrNoChanges, fiber.StatusConflict, "NO_CHANGES"},
	{application.ErrNotOpen, fiber.StatusConflict, "NOT_OPEN"},
	{application.ErrDraft, fiber.StatusConflict, "DRAFT"},
	{application.ErrTargetMoved, fiber.StatusConflict, "TARGET_MOVED"},
	{application.ErrGitArchived, fiber.StatusConflict, "REPOSITORY_ARCHIVED"},
	{gitmerge.ErrMethod, fiber.StatusBadRequest, "INVALID_MERGE_METHOD"},
	{gitmerge.ErrRebaseMerges, fiber.StatusConflict, "REBASE_MERGE_COMMITS"},
	{application.ErrNamespaceNotEmpty, fiber.StatusConflict, "NAMESPACE_NOT_EMPTY"},
	{domain.ErrInvalidNamespaceName, fiber.StatusBadRequest, "INVALID_NAMESPACE_NAME"},
	{application.ErrInvalidName, fiber.StatusBadRequest, "INVALID_NAME"},
	{application.ErrInvalidSSHKey, fiber.StatusBadRequest, "INVALID_SSH_KEY"},
	{application.ErrSSHKeyInUse, fiber.StatusConflict, "SSH_KEY_IN_USE"},
	{application.ErrSSHKeyName, fiber.StatusConflict, "SSH_KEY_NAME_TAKEN"},
	{application.ErrSSHKeyNotFound, fiber.StatusNotFound, "SSH_KEY_NOT_FOUND"},
}

// From converts err into an error response. Known application errors keep
// their message, including wrapped details the application added (e.g. the
// repositories listed by ErrWouldOrphan). Unknown errors are logged and
// returned as a generic 500 without their message.
func From(err error, logger zerolog.Logger) *fiberoapi.ErrorResponse {
	var conflict *application.MergeConflictError
	if errors.As(err, &conflict) {
		return &fiberoapi.ErrorResponse{Code: fiber.StatusConflict, Details: err.Error(), Type: "MERGE_CONFLICT"}
	}
	for _, m := range mapping {
		if errors.Is(err, m.err) {
			return &fiberoapi.ErrorResponse{Code: m.code, Details: err.Error(), Type: m.typ}
		}
	}
	logger.Error().Err(err).Msg("request failed")
	return &fiberoapi.ErrorResponse{Code: fiber.StatusInternalServerError, Details: "internal error", Type: "INTERNAL_ERROR"}
}

// Unauthenticated is returned when a handler finds no auth context.
func Unauthenticated() *fiberoapi.ErrorResponse {
	return &fiberoapi.ErrorResponse{Code: fiber.StatusUnauthorized, Details: "Authentication required", Type: "AUTHENTICATION_REQUIRED"}
}
