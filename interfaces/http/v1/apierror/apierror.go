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
	{application.ErrGroupHasSubgroups, fiber.StatusConflict, "GROUP_HAS_SUBGROUPS"},
	{application.ErrInheritedRole, fiber.StatusConflict, "INHERITED_ROLE"},
	{application.ErrGroupMoveIntoItself, fiber.StatusBadRequest, "MOVE_INTO_ITSELF"},
	{application.ErrTopLevelRename, fiber.StatusBadRequest, "TOP_LEVEL_RENAME"},
	{application.ErrGroupTooDeep, fiber.StatusBadRequest, "GROUP_TOO_DEEP"},
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
	{application.ErrTwoFactorRequired, fiber.StatusForbidden, "TWO_FACTOR_REQUIRED"},
	{application.ErrAdminNeedsTwoFactor, fiber.StatusForbidden, "ADMIN_TWO_FACTOR_REQUIRED"},
	{application.ErrGroupRequiresTwoFactor, fiber.StatusForbidden, "GROUP_REQUIRES_TWO_FACTOR"},
	{application.ErrWebAuthnUnavailable, fiber.StatusConflict, "WEBAUTHN_UNAVAILABLE"},
	{application.ErrInvalidKey, fiber.StatusUnauthorized, "INVALID_KEY"},
	{application.ErrKeyNotFound, fiber.StatusNotFound, "KEY_NOT_FOUND"},
	{application.ErrKeyNameTaken, fiber.StatusConflict, "KEY_NAME_TAKEN"},
	{application.ErrInvalidCode, fiber.StatusUnauthorized, "INVALID_CODE"},
	{application.ErrChallengeExpired, fiber.StatusUnauthorized, "CHALLENGE_EXPIRED"},
	{application.ErrTwoFactorEnabled, fiber.StatusConflict, "TWO_FACTOR_ENABLED"},
	{application.ErrTwoFactorDisabled, fiber.StatusConflict, "TWO_FACTOR_DISABLED"},
	{application.ErrTwoFactorUnavailable, fiber.StatusConflict, "TWO_FACTOR_UNAVAILABLE"},
	{application.ErrNoPendingSetup, fiber.StatusConflict, "NO_PENDING_SETUP"},
	{application.ErrAccountDisabled, fiber.StatusForbidden, "ACCOUNT_DISABLED"},
	{application.ErrNotificationNotFound, fiber.StatusNotFound, "NOTIFICATION_NOT_FOUND"},
	{application.ErrInvalidWatch, fiber.StatusBadRequest, "INVALID_WATCH_LEVEL"},
	{application.ErrInvalidEmailLevel, fiber.StatusBadRequest, "INVALID_EMAIL_LEVEL"},
	{application.ErrReleaseNotFound, fiber.StatusNotFound, "RELEASE_NOT_FOUND"},
	{application.ErrReleaseExists, fiber.StatusConflict, "RELEASE_EXISTS"},
	{application.ErrInvalidRelease, fiber.StatusBadRequest, "INVALID_RELEASE"},
	{application.ErrTagNotFound, fiber.StatusNotFound, "TAG_NOT_FOUND"},
	{application.ErrAssetNotFound, fiber.StatusNotFound, "ASSET_NOT_FOUND"},
	{application.ErrRunnerNotFound, fiber.StatusNotFound, "RUNNER_NOT_FOUND"},
	{application.ErrRunNotFound, fiber.StatusNotFound, "RUN_NOT_FOUND"},
	{application.ErrRunnerScope, fiber.StatusBadRequest, "INVALID_SCOPE"},
	{application.ErrSecretsUnavailable, fiber.StatusConflict, "SECRETS_UNAVAILABLE"},
	{application.ErrInvalidSecretName, fiber.StatusBadRequest, "INVALID_NAME"},
	{application.ErrSecretTooLarge, fiber.StatusRequestEntityTooLarge, "TOO_LARGE"},
	{application.ErrSecretNotFound, fiber.StatusNotFound, "NOT_FOUND"},
	{application.ErrWorkflowNotFound, fiber.StatusNotFound, "WORKFLOW_NOT_FOUND"},
	{application.ErrRunNotDone, fiber.StatusConflict, "RUN_NOT_DONE"},
	{application.ErrArtifactNotFound, fiber.StatusNotFound, "ARTIFACT_NOT_FOUND"},
	{application.ErrCacheNotFound, fiber.StatusNotFound, "CACHE_NOT_FOUND"},
	{application.ErrRefNotFoundForDispatch, fiber.StatusNotFound, "REF_NOT_FOUND"},
	{application.ErrInvalidInputs, fiber.StatusUnprocessableEntity, "INVALID_INPUTS"},
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
	{application.ErrInvalidReviewer, fiber.StatusBadRequest, "INVALID_REVIEWER"},
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
	{application.ErrGitMirror, fiber.StatusConflict, "REPOSITORY_MIRROR"},
	{application.ErrWikiNotFound, fiber.StatusNotFound, "WIKI_NOT_FOUND"},
	{application.ErrWikiPageNotFound, fiber.StatusNotFound, "WIKI_PAGE_NOT_FOUND"},
	{application.ErrWikiPageExists, fiber.StatusConflict, "WIKI_PAGE_EXISTS"},
	{application.ErrWikiConflict, fiber.StatusConflict, "WIKI_CONFLICT"},
	{application.ErrWikiInvalidName, fiber.StatusBadRequest, "INVALID_PAGE_NAME"},
	{application.ErrWikiTooLarge, fiber.StatusRequestEntityTooLarge, "PAGE_TOO_LARGE"},
	{application.ErrWikiInvalidFile, fiber.StatusBadRequest, "INVALID_ATTACHMENT"},
	{application.ErrInvalidWikiMode, fiber.StatusBadRequest, "INVALID_WIKI_ACCESS"},
	{application.ErrMirrorNotFound, fiber.StatusNotFound, "MIRROR_NOT_FOUND"},
	{application.ErrInvalidMirror, fiber.StatusBadRequest, "INVALID_MIRROR"},
	{application.ErrMirrorNeedsKey, fiber.StatusConflict, "ENCRYPTION_KEY_REQUIRED"},
	{application.ErrMirrorInProgress, fiber.StatusConflict, "SYNC_IN_PROGRESS"},
	{application.ErrPushMirrorNotFound, fiber.StatusNotFound, "PUSH_MIRROR_NOT_FOUND"},
	{application.ErrTooManyPushMirrors, fiber.StatusConflict, "TOO_MANY_PUSH_MIRRORS"},
	{application.ErrImportNotFound, fiber.StatusNotFound, "IMPORT_NOT_FOUND"},
	{application.ErrInvalidImport, fiber.StatusBadRequest, "INVALID_IMPORT"},
	{application.ErrSSOProviderNotFound, fiber.StatusNotFound, "SSO_PROVIDER_NOT_FOUND"},
	{application.ErrPasswordLoginDisabled, fiber.StatusForbidden, "PASSWORD_LOGIN_DISABLED"},
	{application.ErrSSONoAccount, fiber.StatusForbidden, "NO_ACCOUNT"},
	{application.ErrSSOVerifiedEmail, fiber.StatusForbidden, "EMAIL_REQUIRED"},
	{application.ErrSSOEmailTaken, fiber.StatusConflict, "EMAIL_TAKEN"},
	{application.ErrSSONotAllowed, fiber.StatusForbidden, "NOT_ALLOWED"},
	{application.ErrSSOOnlyNoProvider, fiber.StatusConflict, "NO_SIGN_IN_PROVIDER"},
	{application.ErrSSOOnlyLockout, fiber.StatusConflict, "WOULD_LOCK_OUT"},
	{application.ErrInvalidGroupMapping, fiber.StatusBadRequest, "INVALID_GROUP_MAPPING"},
	{application.ErrInvalidSSOProvider, fiber.StatusBadRequest, "INVALID_SSO_PROVIDER"},
	{application.ErrSSOProviderExists, fiber.StatusConflict, "SSO_PROVIDER_EXISTS"},
	{application.ErrSSOProviderReadOnly, fiber.StatusConflict, "SSO_PROVIDER_READ_ONLY"},
	{application.ErrSSONeedsPublicURL, fiber.StatusConflict, "PUBLIC_URL_REQUIRED"},
	{application.ErrIdentityTaken, fiber.StatusConflict, "IDENTITY_TAKEN"},
	{application.ErrIdentityNotFound, fiber.StatusNotFound, "IDENTITY_NOT_FOUND"},
	{application.ErrLastSignInMethod, fiber.StatusConflict, "LAST_SIGN_IN_METHOD"},
	{application.ErrLinkExpired, fiber.StatusNotFound, "LINK_EXPIRED"},
	{application.ErrLinkNeedsCode, fiber.StatusUnauthorized, "TWO_FACTOR_CODE_REQUIRED"},
	{gitmerge.ErrMethod, fiber.StatusBadRequest, "INVALID_MERGE_METHOD"},
	{gitmerge.ErrRebaseMerges, fiber.StatusConflict, "REBASE_MERGE_COMMITS"},
	{application.ErrNamespaceNotEmpty, fiber.StatusConflict, "NAMESPACE_NOT_EMPTY"},
	{application.ErrNamespaceHasPackages, fiber.StatusConflict, "NAMESPACE_HAS_PACKAGES"},
	{domain.ErrInvalidNamespaceName, fiber.StatusBadRequest, "INVALID_NAMESPACE_NAME"},
	{application.ErrInvalidName, fiber.StatusBadRequest, "INVALID_NAME"},
	{application.ErrInvalidSSHKey, fiber.StatusBadRequest, "INVALID_SSH_KEY"},
	{application.ErrSSHKeyInUse, fiber.StatusConflict, "SSH_KEY_IN_USE"},
	{application.ErrSSHKeyName, fiber.StatusConflict, "SSH_KEY_NAME_TAKEN"},
	{application.ErrSSHKeyNotFound, fiber.StatusNotFound, "SSH_KEY_NOT_FOUND"},
	{application.ErrPackageNotFound, fiber.StatusNotFound, "PACKAGE_NOT_FOUND"},
	{application.ErrGoModuleNotFound, fiber.StatusNotFound, "GO_MODULE_NOT_FOUND"},
	{application.ErrPackageVersionExists, fiber.StatusConflict, "PACKAGE_FILE_EXISTS"},
	{application.ErrInvalidPackage, fiber.StatusBadRequest, "INVALID_PACKAGE"},
	{application.ErrPackageTooLarge, fiber.StatusRequestEntityTooLarge, "PACKAGE_TOO_LARGE"},
	{application.ErrPackageScope, fiber.StatusForbidden, "TOKEN_SCOPE"},
	{application.ErrInvalidVisibility, fiber.StatusBadRequest, "INVALID_VISIBILITY"},
	{application.ErrPackageLink, fiber.StatusBadRequest, "INVALID_REPOSITORY"},
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
