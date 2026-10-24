// Package sshkey is the SSH key API: the caller's keys
// (/api/v1/user/ssh-keys), a repository's deploy keys
// (/api/v1/repository/:identifier/deploy-keys) and admin revocation
// (/api/v1/admin/users/:username/ssh-keys).
package sshkey

import (
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/helpers/validator"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/sshkey/dtos"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger    zerolog.Logger
	SSHKeyApp *application.SSHKeyApp
}

func keyResponse(k *domain.SSHKey) dtos.SSHKeyResponse {
	resp := dtos.SSHKeyResponse{Id: k.Id, Name: k.Name, Fingerprint: k.Fingerprint, Type: string(k.KeyType), LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt}
	if k.OwnerType == domain.SSHKeyTypeRepository {
		ro := k.ReadOnly
		resp.ReadOnly = &ro
	}
	return resp
}

func listResponse(keys []domain.SSHKey) *dtos.ListSSHKeysResponse {
	resp := &dtos.ListSSHKeysResponse{Keys: make([]dtos.SSHKeyResponse, 0, len(keys))}
	for i := range keys {
		resp.Keys = append(resp.Keys, keyResponse(&keys[i]))
	}
	return resp
}

func splitIdentifier(identifier string) (id, slug *string) {
	if validator.IsValidUUID(identifier) {
		return &identifier, nil
	}
	return nil, &identifier
}

func (ctrl *Controller) ListUserKeys(ctx *fiber.Ctx, _ struct{}) (*dtos.ListSSHKeysResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	keys, err := ctrl.SSHKeyApp.ListUserKeys(auth.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return listResponse(keys), nil
}

func (ctrl *Controller) AddUserKey(ctx *fiber.Ctx, req dtos.AddUserKeyRequest) (*dtos.SSHKeyResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	key, err := ctrl.SSHKeyApp.AddUserKey(auth.UserID, req.Name, req.Key)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := keyResponse(key)
	return &resp, nil
}

func (ctrl *Controller) DeleteUserKey(ctx *fiber.Ctx, req dtos.DeleteUserKeyRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.SSHKeyApp.DeleteUserKey(auth.UserID, req.KeyId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "SSH key deleted"}, nil
}

func (ctrl *Controller) ListDeployKeys(ctx *fiber.Ctx, req dtos.RepositoryRequest) (*dtos.ListSSHKeysResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, slug := splitIdentifier(req.Identifier)
	keys, err := ctrl.SSHKeyApp.ListDeployKeys(id, slug, auth.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return listResponse(keys), nil
}

func (ctrl *Controller) AddDeployKey(ctx *fiber.Ctx, req dtos.AddDeployKeyRequest) (*dtos.SSHKeyResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	readOnly := true
	if req.ReadOnly != nil {
		readOnly = *req.ReadOnly
	}
	id, slug := splitIdentifier(req.Identifier)
	key, err := ctrl.SSHKeyApp.AddDeployKey(id, slug, auth.UserID, req.Name, req.Key, readOnly)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := keyResponse(key)
	return &resp, nil
}

func (ctrl *Controller) DeleteDeployKey(ctx *fiber.Ctx, req dtos.DeleteDeployKeyRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, slug := splitIdentifier(req.Identifier)
	if err := ctrl.SSHKeyApp.DeleteDeployKey(id, slug, auth.UserID, req.KeyId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "deploy key deleted"}, nil
}

func (ctrl *Controller) AdminListUserKeys(ctx *fiber.Ctx, req dtos.AdminUserKeysRequest) (*dtos.ListSSHKeysResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	keys, err := ctrl.SSHKeyApp.AdminListUserKeys(auth.UserID, req.Username)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return listResponse(keys), nil
}

func (ctrl *Controller) AdminDeleteUserKey(ctx *fiber.Ctx, req dtos.AdminDeleteUserKeyRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.SSHKeyApp.AdminDeleteUserKey(auth.UserID, req.Username, req.KeyId); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "SSH key deleted"}, nil
}

// Setup registers the routes on the user, repository and admin groups.
func Setup(c Controller, user, repository, admin *fiberoapi.OApiGroup) {
	userTags, repoTags, adminTags := []string{"SSH keys"}, []string{"Deploy keys"}, []string{"Administration"}

	fiberoapi.Get(user, "/ssh-keys", c.ListUserKeys, fiberoapi.OpenAPIOptions{Summary: "List my SSH keys", OperationID: "user.sshKeys.list", Tags: userTags})
	fiberoapi.Post(user, "/ssh-keys", c.AddUserKey, fiberoapi.OpenAPIOptions{
		Summary: "Add an SSH key", OperationID: "user.sshKeys.add", Tags: userTags,
		Description: "One authorized_keys line: ed25519, ECDSA or RSA >= 3072 bits (FIDO sk- keys too). The name defaults to the key comment. A key can be used in one place only.",
	})
	fiberoapi.Delete(user, "/ssh-keys/:key_id", c.DeleteUserKey, fiberoapi.OpenAPIOptions{Summary: "Delete an SSH key", OperationID: "user.sshKeys.delete", Tags: userTags})

	fiberoapi.Get(repository, "/:identifier/deploy-keys", c.ListDeployKeys, fiberoapi.OpenAPIOptions{
		Summary: "List deploy keys", OperationID: "repository.deployKeys.list", Tags: repoTags, Description: "Maintainers and owners.",
	})
	fiberoapi.Post(repository, "/:identifier/deploy-keys", c.AddDeployKey, fiberoapi.OpenAPIOptions{
		Summary: "Add a deploy key", OperationID: "repository.deployKeys.add", Tags: repoTags,
		Description: "Maintainers and owners. read_only defaults to true; false allows pushing.",
	})
	fiberoapi.Delete(repository, "/:identifier/deploy-keys/:key_id", c.DeleteDeployKey, fiberoapi.OpenAPIOptions{
		Summary: "Delete a deploy key", OperationID: "repository.deployKeys.delete", Tags: repoTags, Description: "Maintainers and owners.",
	})

	fiberoapi.Get(admin, "/users/:username/ssh-keys", c.AdminListUserKeys, fiberoapi.OpenAPIOptions{
		Summary: "List a user's SSH keys", OperationID: "admin.users.sshKeys.list", Tags: adminTags,
	})
	fiberoapi.Delete(admin, "/users/:username/ssh-keys/:key_id", c.AdminDeleteUserKey, fiberoapi.OpenAPIOptions{
		Summary: "Revoke a user's SSH key", OperationID: "admin.users.sshKeys.delete", Tags: adminTags,
	})
}
