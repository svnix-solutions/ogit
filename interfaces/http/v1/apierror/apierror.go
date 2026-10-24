// Package apierror maps application errors to API error responses.
package apierror

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
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
