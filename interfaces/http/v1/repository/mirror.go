package repository

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/mirror"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/labbs/ogit/interfaces/http/v1/repository/dtos"
)

// Mirror is a repository's pull mirror (or import in progress), without
// its secrets.
type Mirror struct {
	URL             string     `json:"url"`
	Mirror          bool       `json:"mirror"` // false: a one-time import
	Auth            string     `json:"auth"`   // none, password or ssh
	Username        string     `json:"username,omitempty"`
	HasPassword     bool       `json:"has_password"`
	SSHPublicKey    string     `json:"ssh_public_key,omitempty"` // add it on the remote (as a deploy key)
	SSHHostKey      string     `json:"ssh_host_key,omitempty"`   // pinned on first contact
	LFS             bool       `json:"lfs"`
	IntervalSeconds int        `json:"interval_seconds"`
	Status          string     `json:"status"` // pending, syncing, ok, failed
	LastError       string     `json:"last_error,omitempty"`
	LastSyncAt      *time.Time `json:"last_sync_at"`
	LastSuccessAt   *time.Time `json:"last_success_at"`
	NextSyncAt      time.Time  `json:"next_sync_at"`
}

func toMirror(m *domain.Mirror) *Mirror {
	return &Mirror{URL: mirror.SafeURL(m.URL), Mirror: m.Mirror, Auth: m.Auth, Username: m.Username,
		HasPassword: m.Auth == domain.MirrorAuthPassword && m.Secret != "", SSHPublicKey: m.SSHPublicKey, SSHHostKey: m.SSHHostKey,
		LFS: m.LFS, IntervalSeconds: m.IntervalSeconds, Status: m.Status, LastError: m.LastError,
		LastSyncAt: m.LastSyncAt, LastSuccessAt: m.LastSuccessAt, NextSyncAt: m.NextSyncAt}
}

type MirrorFields struct {
	URL      *string `json:"url"`
	Username *string `json:"username"`
	Password *string `json:"password"` // write-only; "" removes it
	// RegenerateKey: a new SSH key pair (then add its public key on the
	// remote).
	RegenerateKey   bool  `json:"regenerate_key"`
	LFS             *bool `json:"lfs"`
	IntervalSeconds *int  `json:"interval_seconds"`
}

func (f MirrorFields) input() application.MirrorInput {
	in := application.MirrorInput{URL: f.URL, Username: f.Username, Password: f.Password, SSH: f.RegenerateKey, LFS: f.LFS}
	if f.IntervalSeconds != nil {
		d := time.Duration(*f.IntervalSeconds) * time.Second
		in.Interval = &d
	}
	return in
}

type ImportRequest struct {
	Namespace   string `json:"namespace"` // default: the caller's
	Name        string `json:"name" validate:"required"`
	Description string `json:"description"`
	IsPrivate   *bool  `json:"is_private"` // default true
	Mirror      bool   `json:"mirror"`     // keep syncing (read-only)
	MirrorFields
	// Forge also imports issues, pull requests, labels, milestones and
	// releases from the forge's API: github, gitlab or gitea.
	Forge  string `json:"forge"`
	APIURL string `json:"api_url"` // default: from the remote URL
	// APIToken: default the password.
	APIToken *string `json:"api_token"`
}

type ImportResponse struct {
	Repository dtos.GetRepositoryResponse `json:"repository"`
	Mirror     *Mirror                    `json:"mirror"`
}

// APIImport is a repository's import from a forge's API.
type APIImport struct {
	Forge      string     `json:"forge"`
	APIURL     string     `json:"api_url"`
	Project    string     `json:"project"`
	Status     string     `json:"status"` // waiting (for the git data), pending, running, done, failed
	Stage      string     `json:"stage"`  // labels, milestones, issues, pulls, releases, done
	Issues     int        `json:"issues"`
	Pulls      int        `json:"pulls"`
	Releases   int        `json:"releases"`
	Comments   int        `json:"comments"`
	LastError  string     `json:"last_error,omitempty"`
	Warnings   []string   `json:"warnings"`
	NextRunAt  time.Time  `json:"next_run_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

func toAPIImport(i *domain.Import) *APIImport {
	out := &APIImport{Forge: i.Forge, APIURL: i.APIURL, Project: i.Project, Status: i.Status, Stage: i.Stage, Issues: i.Issues, Pulls: i.Pulls,
		Releases: i.Releases, Comments: i.Comments, LastError: i.LastError, Warnings: []string{}, NextRunAt: i.NextRunAt, FinishedAt: i.FinishedAt}
	if i.Warnings != "" {
		out.Warnings = strings.Split(i.Warnings, "\n")
	}
	return out
}

type MirrorRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type UpdateMirrorRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	MirrorFields
}

type MirrorMessage struct {
	Message string `json:"message"`
}

func setupMirrorRoutes(c Controller) {
	tags := []string{"Mirrors"}
	fiberoapi.Post(c.FiberOapi, "/import", c.Import, fiberoapi.OpenAPIOptions{
		Summary: "Import a repository", OperationID: "repository.import", Tags: tags,
		Description: "Creates a repository (where you may create one) from a remote: https://, http://, ssh:// or git@host:path, with an optional username and password or token " +
			"(SSH remotes get a generated key: add the answer's ssh_public_key on the remote, as a deploy key). mirror: keep syncing (read-only) instead of a one-time import. " +
			"lfs: fetch Git LFS objects too. forge (github, gitlab or gitea; api_url and api_token optional) also imports issues, pull requests, labels, milestones and releases once the git data is in: follow GET /:identifier/import. " +
			"The first sync starts in the background: follow GET /:identifier/mirror.",
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier/import", c.GetAPIImport, fiberoapi.OpenAPIOptions{
		Summary: "Get a repository's API import", OperationID: "repository.import.get", Tags: tags,
		Description: "Maintainers and owners: the import of issues, pull requests, labels, milestones and releases (status, stage, counts, warnings, last error).",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/import/retry", c.RetryAPIImport, fiberoapi.OpenAPIOptions{
		Summary: "Retry a failed API import", OperationID: "repository.import.retry", Tags: tags,
		Description: "Maintainers and owners. It resumes where it stopped (imported items are kept).",
	})
	fiberoapi.Delete(c.FiberOapi, "/:identifier/import", c.CancelAPIImport, fiberoapi.OpenAPIOptions{
		Summary: "Cancel an API import", OperationID: "repository.import.delete", Tags: tags,
		Description: "Owners. What was imported stays.",
	})
	fiberoapi.Get(c.FiberOapi, "/:identifier/mirror", c.GetMirror, fiberoapi.OpenAPIOptions{
		Summary: "Get a repository's mirror", OperationID: "repository.mirror.get", Tags: tags,
		Description: "Maintainers and owners: the remote, status, last error and SSH public key (never the password).",
	})
	fiberoapi.Patch(c.FiberOapi, "/:identifier/mirror", c.UpdateMirror, fiberoapi.OpenAPIOptions{
		Summary: "Change a repository's mirror", OperationID: "repository.mirror.update", Tags: tags,
		Description: "Maintainers and owners. A new URL or credentials sync at once; interval_seconds at least mirrors.min_interval.",
	})
	fiberoapi.Post(c.FiberOapi, "/:identifier/mirror/sync", c.SyncMirror, fiberoapi.OpenAPIOptions{
		Summary: "Sync a mirror now", OperationID: "repository.mirror.sync", Tags: tags,
	})
	fiberoapi.Delete(c.FiberOapi, "/:identifier/mirror", c.StopMirror, fiberoapi.OpenAPIOptions{
		Summary: "Stop mirroring", OperationID: "repository.mirror.delete", Tags: tags,
		Description: "Owners. The repository becomes a normal one (pushes allowed); the remote and its credentials are forgotten.",
	})
}

func (ctrl *Controller) Import(ctx *fiber.Ctx, req ImportRequest) (*ImportResponse, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	private := true
	if req.IsPrivate != nil {
		private = *req.IsPrivate
	}
	in := application.ImportInput{Namespace: req.Namespace, Name: req.Name,
		Description: req.Description, Private: private, Mirror: req.Mirror, MirrorInput: req.MirrorFields.input()}
	if req.Forge != "" {
		in.API = &application.APIImportInput{Forge: req.Forge, APIURL: req.APIURL, Token: req.APIToken}
	}
	repo, m, err := ctrl.MirrorApp.Import(authCtx.UserID, in)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &ImportResponse{Repository: *repositoryResponse(repo), Mirror: toMirror(m)}, nil
}

func (ctrl *Controller) GetMirror(ctx *fiber.Ctx, req MirrorRequest) (*Mirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	_, m, err := ctrl.MirrorApp.Get(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return toMirror(m), nil
}

func (ctrl *Controller) UpdateMirror(ctx *fiber.Ctx, req UpdateMirrorRequest) (*Mirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	m, err := ctrl.MirrorApp.Update(id, path, authCtx.UserID, req.MirrorFields.input())
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return toMirror(m), nil
}

func (ctrl *Controller) SyncMirror(ctx *fiber.Ctx, req MirrorRequest) (*Mirror, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	m, err := ctrl.MirrorApp.SyncNow(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return toMirror(m), nil
}

func (ctrl *Controller) StopMirror(ctx *fiber.Ctx, req MirrorRequest) (*MirrorMessage, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.MirrorApp.Stop(id, path, authCtx.UserID); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &MirrorMessage{Message: "mirroring stopped"}, nil
}

func (ctrl *Controller) GetAPIImport(ctx *fiber.Ctx, req MirrorRequest) (*APIImport, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	imp, err := ctrl.ImportApp.Get(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return toAPIImport(imp), nil
}

func (ctrl *Controller) RetryAPIImport(ctx *fiber.Ctx, req MirrorRequest) (*APIImport, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	imp, err := ctrl.ImportApp.Retry(id, path, authCtx.UserID)
	if err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return toAPIImport(imp), nil
}

func (ctrl *Controller) CancelAPIImport(ctx *fiber.Ctx, req MirrorRequest) (*MirrorMessage, *fiberoapi.ErrorResponse) {
	authCtx, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return nil, apierror.Unauthenticated()
	}
	id, path := repoid.Split(req.Identifier)
	if err := ctrl.ImportApp.Cancel(id, path, authCtx.UserID); err != nil {
		return nil, apierror.From(err, ctrl.Logger)
	}
	return &MirrorMessage{Message: "import cancelled"}, nil
}
