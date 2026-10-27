package repository

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/mirror"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

// PushMirror is a repository's push mirror, without its secrets.
type PushMirror struct {
	Id              string     `json:"id"`
	URL             string     `json:"url"`
	Mode            string     `json:"mode"` // mirror (force, prune) or branches
	Auth            string     `json:"auth"` // none, password or ssh
	Username        string     `json:"username,omitempty"`
	HasPassword     bool       `json:"has_password"`
	SSHPublicKey    string     `json:"ssh_public_key,omitempty"` // add it on the remote, with write access
	SSHHostKey      string     `json:"ssh_host_key,omitempty"`   // pinned on first contact
	LFS             bool       `json:"lfs"`
	IntervalSeconds int        `json:"interval_seconds"`
	Status          string     `json:"status"` // pending, syncing, ok, failed
	LastError       string     `json:"last_error,omitempty"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	NextSyncAt      time.Time  `json:"next_sync_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

func toPushMirror(m *domain.PushMirror) PushMirror {
	return PushMirror{Id: m.Id, URL: mirror.SafeURL(m.URL), Mode: m.Mode, Auth: m.Auth, Username: m.Username,
		HasPassword: m.Auth == domain.MirrorAuthPassword && m.Secret != "", SSHPublicKey: m.SSHPublicKey, SSHHostKey: m.SSHHostKey,
		LFS: m.LFS, IntervalSeconds: m.IntervalSeconds, Status: m.Status, LastError: m.LastError,
		LastSyncAt: m.LastSyncAt, LastSuccessAt: m.LastSuccessAt, NextSyncAt: m.NextSyncAt, CreatedAt: m.CreatedAt}
}

type PushMirrorFields struct {
	MirrorFields
	Mode *string `json:"mode"` // mirror (default) or branches
}

func (f PushMirrorFields) input() application.PushMirrorInput {
	return application.PushMirrorInput{MirrorInput: f.MirrorFields.input(), Mode: f.Mode}
}

type PushMirrorsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type PushMirrorsResponse struct {
	PushMirrors []PushMirror `json:"push_mirrors"`
}

type CreatePushMirrorRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	PushMirrorFields
}

type PushMirrorRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Mirror     string `path:"mirror" json:"-" validate:"required"`
}

type UpdatePushMirrorRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Mirror     string `path:"mirror" json:"-" validate:"required"`
	PushMirrorFields
}

func setupPushMirrorRoutes(c Controller) {
	tags := []string{"Mirrors"}
	fiberoapi.Get(c.FiberOapi, "/:identifier/push-mirrors", c.ListPushMirrors, fiberoapi.OpenAPIOptions{
		Summary: "List a repository's push mirrors", OperationID: "repository.push_mirrors.list", Tags: tags,
		Description: "Maintainers and owners: remotes, modes, statuses and SSH public keys (never the passwords).",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/push-mirrors", c.CreatePushMirror, fiberoapi.OpenAPIOptions{
		Summary: "Add a push mirror", OperationID: "repository.push_mirrors.create", Tags: tags,
		Description: "Maintainers and owners, at most 10 per repository. The repository's branches and tags are pushed to the remote after every push and every interval_seconds. " +
			"url: https://, http:// (optional username and password or token) or ssh:// and git@host:path (a key is generated: add the answer's ssh_public_key on the remote with write access). " +
			"mode: mirror (default; forced, the remote's other branches and tags deleted) or branches (never forced or deleted; diverged refs are reported). lfs: upload Git LFS objects too.",
	})
	fiberoapi.Patch(c.FiberOapi, "/:identifier/push-mirrors/:mirror", c.UpdatePushMirror, fiberoapi.OpenAPIOptions{
		Summary: "Change a push mirror", OperationID: "repository.push_mirrors.update", Tags: tags,
		Description: "Maintainers and owners. A new URL, credentials, mode or lfs setting sync at once.",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/push-mirrors/:mirror/sync", c.SyncPushMirror, fiberoapi.OpenAPIOptions{
		Summary: "Push to a mirror now", OperationID: "repository.push_mirrors.sync", Tags: tags,
	})
	fiberoapi.Delete(c.FiberOapi, "/:identifier/push-mirrors/:mirror", c.DeletePushMirror, fiberoapi.OpenAPIOptions{
		Summary: "Remove a push mirror", OperationID: "repository.push_mirrors.delete", Tags: tags,
		Description: "Maintainers and owners. The remote keeps what it got.",
	})
}

func (ctrl *Controller) ListPushMirrors(ctx *fiber.Ctx, req PushMirrorsRequest) (*PushMirrorsResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	list, err := ctrl.PushMirrorApp.List(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := &PushMirrorsResponse{PushMirrors: make([]PushMirror, len(list))}
	for i := range list {
		out.PushMirrors[i] = toPushMirror(&list[i])
	}
	return out, nil
}

func (ctrl *Controller) CreatePushMirror(ctx *fiber.Ctx, req CreatePushMirrorRequest) (*PushMirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	m, err := ctrl.PushMirrorApp.Create(id, path, authCtx.UserID, req.PushMirrorFields.input())
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toPushMirror(m)
	return &out, nil
}

func (ctrl *Controller) UpdatePushMirror(ctx *fiber.Ctx, req UpdatePushMirrorRequest) (*PushMirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	m, err := ctrl.PushMirrorApp.Update(id, path, req.Mirror, authCtx.UserID, req.PushMirrorFields.input())
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toPushMirror(m)
	return &out, nil
}

func (ctrl *Controller) SyncPushMirror(ctx *fiber.Ctx, req PushMirrorRequest) (*PushMirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	m, err := ctrl.PushMirrorApp.SyncNow(id, path, req.Mirror, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	out := toPushMirror(m)
	return &out, nil
}

func (ctrl *Controller) DeletePushMirror(ctx *fiber.Ctx, req PushMirrorRequest) (*MirrorMessage, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.PushMirrorApp.Delete(id, path, req.Mirror, authCtx.UserID); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &MirrorMessage{Message: "push mirror removed"}, nil
}
