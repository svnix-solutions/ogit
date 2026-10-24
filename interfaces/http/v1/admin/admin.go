// Package admin is the site administration API (/api/v1/admin). Every
// operation checks that the caller is an active admin (application.AdminApp).
package admin

import (
	"net/url"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/admin/dtos"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger    zerolog.Logger
	FiberOapi *fiberoapi.OApiGroup
	AdminApp  *application.AdminApp
}

func userResponse(u *domain.User) dtos.UserResponse {
	role := string(u.Role)
	if role == "" {
		role = string(domain.RoleUser)
	}
	return dtos.UserResponse{Id: u.Id, Username: u.Username, Email: u.Email, Role: role,
		Active: u.Active, EmailVerifiedAt: u.EmailVerifiedAt, CreatedAt: u.CreatedAt}
}

func (ctrl *Controller) ListUsers(ctx *fiber.Ctx, req dtos.ListUsersRequest) (*dtos.ListUsersResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	users, total, err := ctrl.AdminApp.ListUsers(auth.UserID, req.Query, req.Limit, req.Offset)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListUsersResponse{Users: make([]dtos.UserResponse, 0, len(users)), Total: total}
	for i := range users {
		resp.Users = append(resp.Users, userResponse(&users[i]))
	}
	return resp, nil
}

func (ctrl *Controller) CreateUser(ctx *fiber.Ctx, req dtos.CreateUserRequest) (*dtos.UserResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	user, err := ctrl.AdminApp.CreateUser(auth.UserID, req.Username, req.Email, req.Password)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := userResponse(user)
	return &resp, nil
}

func (ctrl *Controller) UpdateUser(ctx *fiber.Ctx, req dtos.UpdateUserRequest) (*dtos.UserResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	upd := application.UserUpdate{Active: req.Active, EmailVerified: req.EmailVerified}
	if req.Role != nil {
		role := domain.Role(*req.Role)
		upd.Role = &role
	}
	user, err := ctrl.AdminApp.UpdateUser(auth.UserID, req.Username, upd)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := userResponse(user)
	return &resp, nil
}

func (ctrl *Controller) DeleteUser(ctx *fiber.Ctx, req dtos.UserRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.AdminApp.DeleteUser(auth.UserID, req.Username); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "user deleted"}, nil
}

func (ctrl *Controller) ListRepositories(ctx *fiber.Ctx, req dtos.ListRepositoriesRequest) (*dtos.ListRepositoriesResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	repos, total, err := ctrl.AdminApp.ListRepositories(auth.UserID, req.Limit, req.Offset)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	resp := &dtos.ListRepositoriesResponse{Repositories: make([]dtos.RepositoryResponse, 0, len(repos)), Total: total}
	for _, r := range repos {
		resp.Repositories = append(resp.Repositories, dtos.RepositoryResponse{Id: r.Id, Namespace: r.Namespace, Slug: r.Slug, FullPath: r.FullPath(), Name: r.Name, IsPrivate: r.IsPrivate, CreatedAt: r.CreatedAt})
	}
	return resp, nil
}

func (ctrl *Controller) SetRepositoryOwner(ctx *fiber.Ctx, req dtos.SetOwnerRequest) (*dtos.SetOwnerResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	member, err := ctrl.AdminApp.SetRepositoryOwner(auth.UserID, decoded(req.Identifier), req.Username)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.SetOwnerResponse{MemberId: member.Id, Username: member.User.Username, Role: string(member.AccessType)}, nil
}

func (ctrl *Controller) TransferRepository(ctx *fiber.Ctx, req dtos.TransferRepositoryRequest) (*dtos.RepositoryResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	r, err := ctrl.AdminApp.TransferRepository(auth.UserID, decoded(req.Identifier), req.Namespace)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.RepositoryResponse{Id: r.Id, Namespace: r.Namespace, Slug: r.Slug, FullPath: r.FullPath(), Name: r.Name, IsPrivate: r.IsPrivate, CreatedAt: r.CreatedAt}, nil
}

func (ctrl *Controller) DeleteRepository(ctx *fiber.Ctx, req dtos.DeleteRepositoryRequest) (*dtos.MessageResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	if err := ctrl.AdminApp.DeleteRepository(auth.UserID, decoded(req.Identifier), req.Confirm); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.MessageResponse{Message: "repository deleted"}, nil
}

func (ctrl *Controller) TransferGroup(ctx *fiber.Ctx, req dtos.TransferGroupRequest) (*dtos.GroupResponse, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	group, err := ctrl.AdminApp.TransferGroup(auth.UserID, req.Identifier, req.Username)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &dtos.GroupResponse{Id: group.Id, Name: group.Name, OwnerId: group.OwnerId}, nil
}

func SetupAdminRouter(c Controller) {
	tags := []string{"Administration"}
	fiberoapi.Get(c.FiberOapi, "/users", c.ListUsers, fiberoapi.OpenAPIOptions{
		Summary: "List users", OperationID: "admin.users.list", Tags: tags,
		Description: "Admins only. ?q= matches username or email; ?limit= (max 200, default 50) and ?offset=.",
	})
	fiberoapi.Post(c.FiberOapi, "/users", c.CreateUser, fiberoapi.OpenAPIOptions{
		Summary: "Create user", OperationID: "admin.users.create", Tags: tags,
		Description: "Admins only. Creates a verified account with an initial password (password policy applies; registration settings do not).",
	})
	fiberoapi.Patch(c.FiberOapi, "/users/:username", c.UpdateUser, fiberoapi.OpenAPIOptions{
		Summary: "Update user", OperationID: "admin.users.update", Tags: tags,
		Description: "Admins only. Set active (disabling ends all sessions immediately), email_verified: true, or role (admin/user). The last active admin cannot be demoted or disabled.",
	})
	fiberoapi.Delete(c.FiberOapi, "/users/:username", c.DeleteUser, fiberoapi.OpenAPIOptions{
		Summary: "Delete user", OperationID: "admin.users.delete", Tags: tags,
		Description: "Admins only. Refused while the user is the sole owner of a repository, has repositories in their namespace, or owns a group (the response lists them), or is the last active admin.",
	})
	fiberoapi.Get(c.FiberOapi, "/repositories", c.ListRepositories, fiberoapi.OpenAPIOptions{
		Summary: "List all repositories", OperationID: "admin.repositories.list", Tags: tags,
	})
	fiberoapi.Post(c.FiberOapi, "/repositories/:identifier/owner", c.SetRepositoryOwner, fiberoapi.OpenAPIOptions{
		Summary: "Make a user owner of a repository", OperationID: "admin.repositories.owner", Tags: tags,
		Description: "Admins only. Adds the user as owner, or upgrades their membership. Use it to recover repositories whose owners are gone.",
	})
	fiberoapi.Post(c.FiberOapi, "/repositories/:identifier/transfer", c.TransferRepository, fiberoapi.OpenAPIOptions{
		Summary: "Move a repository to another namespace", OperationID: "admin.repositories.transfer", Tags: tags,
		Description: "Admins only. Moves the repository to any user's or group's namespace; the old path redirects. Use it before deleting a user whose namespace holds repositories.",
	})
	fiberoapi.Delete(c.FiberOapi, "/repositories/:identifier", c.DeleteRepository, fiberoapi.OpenAPIOptions{
		Summary: "Delete a repository", OperationID: "admin.repositories.delete", Tags: tags,
		Description: "Admins only. Permanently deletes the repository and its git data. Requires ?confirm=<slug>.",
	})
	fiberoapi.Post(c.FiberOapi, "/groups/:identifier/transfer", c.TransferGroup, fiberoapi.OpenAPIOptions{
		Summary: "Transfer group ownership", OperationID: "admin.groups.transfer", Tags: tags,
		Description: "Admins only. Makes the user the group owner (adding them if needed); the previous owner becomes a maintainer.",
	})
}

// decoded undoes the URL-encoding of a repository path (alice%2Fsite).
func decoded(identifier string) string {
	if d, err := url.PathUnescape(identifier); err == nil {
		return d
	}
	return identifier
}
