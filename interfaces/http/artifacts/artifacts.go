// Package artifacts serves GitHub's artifact service (v4) for
// actions/upload-artifact and download-artifact: Twirp JSON calls under
// /twirp/github.actions.results.api.v1.ArtifactService/, authenticated by
// the run's ACTIONS_RUNTIME_TOKEN, and the signed URLs the zips go
// through (Azure blob style: blocks, then a block list).
package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/rs/zerolog"
)

const Base = "/twirp/github.actions.results.api.v1.ArtifactService/"

type Controller struct {
	Config  config.Config
	Logger  zerolog.Logger
	Actions *application.ActionsApp
}

func Setup(c Controller, app *fiber.App) {
	for name, h := range map[string]func(*fiber.Ctx, *domain.ActionRun, request) error{
		"CreateArtifact":       c.create,
		"FinalizeArtifact":     c.finalize,
		"ListArtifacts":        c.list,
		"GetSignedArtifactURL": c.signedURL,
		"DeleteArtifact":       c.delete,
	} {
		app.Post(Base+name, c.twirp(h))
	}
	app.Put(Base+"UploadArtifact", c.Upload)
	app.Get(Base+"DownloadArtifact", c.Download)
}

// twirpError answers like Twirp: {"code", "msg"}.
func twirpError(ctx *fiber.Ctx, status int, code, msg string) error {
	return ctx.Status(status).JSON(fiber.Map{"code": code, "msg": msg})
}

func (c Controller) fail(ctx *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, application.ErrRuntimeToken), errors.Is(err, application.ErrArtifactSignature):
		return twirpError(ctx, fiber.StatusUnauthorized, "unauthenticated", err.Error())
	case errors.Is(err, application.ErrArtifactNotFound):
		return twirpError(ctx, fiber.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, application.ErrArtifactExists):
		return twirpError(ctx, fiber.StatusConflict, "already_exists", err.Error())
	case errors.Is(err, application.ErrArtifactTooLarge):
		return twirpError(ctx, fiber.StatusRequestEntityTooLarge, "resource_exhausted", err.Error())
	case errors.Is(err, application.ErrInvalidArtifact), errors.Is(err, application.ErrInvalidBlockList):
		return twirpError(ctx, fiber.StatusBadRequest, "invalid_argument", err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.artifacts").Str("path", ctx.Path()).Msg("artifact request failed")
	return twirpError(ctx, fiber.StatusInternalServerError, "internal", "internal error")
}

// request is a Twirp JSON request. Clients send proto field names or JSON
// names, and 64-bit integers as strings or numbers.
type request map[string]any

func (r request) str(names ...string) string {
	for _, n := range names {
		switch v := r[n].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

func (c Controller) twirp(h func(*fiber.Ctx, *domain.ActionRun, request) error) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		token, _ := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
		run, err := c.Actions.RuntimeRun(token)
		if err != nil {
			return c.fail(ctx, err)
		}
		req := request{}
		if err := json.Unmarshal(ctx.Body(), &req); err != nil {
			return twirpError(ctx, fiber.StatusBadRequest, "malformed", "invalid JSON")
		}
		// A run reaches its own artifacts only.
		if id := req.str("workflow_run_backend_id", "workflowRunBackendId"); id != "" && id != run.Id {
			return c.fail(ctx, application.ErrArtifactNotFound)
		}
		return h(ctx, run, req)
	}
}

func (c Controller) serverURL(ctx *fiber.Ctx) string {
	if u := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); u != "" {
		return u
	}
	return ctx.BaseURL()
}

func (c Controller) create(ctx *fiber.Ctx, run *domain.ActionRun, req request) error {
	var expires *time.Time
	if s := req.str("expires_at", "expiresAt"); s != "" {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			expires = &t
		}
	}
	a, err := c.Actions.CreateArtifact(run, req.str("name"), expires)
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.JSON(fiber.Map{"ok": true, "signedUploadUrl": c.serverURL(ctx) + Base + "UploadArtifact?" + c.Actions.SignArtifactURL(a, "UploadArtifact")})
}

func (c Controller) finalize(ctx *fiber.Ctx, run *domain.ActionRun, req request) error {
	a, err := c.Actions.FinalizeArtifact(run, req.str("name"))
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.JSON(fiber.Map{"ok": true, "artifactId": strconv.FormatInt(a.ApiId, 10)})
}

func artifactJSON(run *domain.ActionRun, a domain.ActionArtifact) fiber.Map {
	return fiber.Map{
		"workflowRunBackendId": run.Id, "workflowJobRunBackendId": run.Id, "databaseId": strconv.FormatInt(a.ApiId, 10),
		"name": a.Name, "size": strconv.FormatInt(a.Size, 10), "createdAt": a.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func (c Controller) list(ctx *fiber.Ctx, run *domain.ActionRun, req request) error {
	list, err := c.Actions.RunArtifacts(run, req.str("name_filter", "nameFilter"))
	if err != nil {
		return c.fail(ctx, err)
	}
	idFilter := req.str("id_filter", "idFilter")
	out := []fiber.Map{}
	for _, a := range list {
		if idFilter == "" || idFilter == strconv.FormatInt(a.ApiId, 10) {
			out = append(out, artifactJSON(run, a))
		}
	}
	return ctx.JSON(fiber.Map{"artifacts": out})
}

func (c Controller) signedURL(ctx *fiber.Ctx, run *domain.ActionRun, req request) error {
	a, err := c.Actions.OwnArtifact(run, req.str("name"))
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.JSON(fiber.Map{"signedUrl": c.serverURL(ctx) + Base + "DownloadArtifact?" + c.Actions.SignArtifactURL(a, "DownloadArtifact")})
}

func (c Controller) delete(ctx *fiber.Ctx, run *domain.ActionRun, req request) error {
	a, err := c.Actions.DeleteOwnArtifact(run, req.str("name"))
	if err != nil {
		return c.fail(ctx, err)
	}
	return ctx.JSON(fiber.Map{"ok": true, "artifactId": strconv.FormatInt(a.ApiId, 10)})
}

// Upload takes the zip through a signed URL: blocks (comp=block&blockid=),
// then their order (comp=blocklist), or the whole zip in one PUT.
func (c Controller) Upload(ctx *fiber.Ctx) error {
	a, err := c.Actions.VerifyArtifactURL("UploadArtifact", ctx.Query("artifact"), ctx.Query("expires"), ctx.Query("sig"))
	if err != nil {
		return c.fail(ctx, err)
	}
	body := io.Reader(ctx.Context().RequestBodyStream())
	if !ctx.Request().IsBodyStream() {
		body = bytes.NewReader(ctx.Body())
	}
	switch ctx.Query("comp") {
	case "block":
		blockId := ctx.Query("blockid")
		if blockId == "" {
			return c.fail(ctx, application.ErrInvalidBlockList)
		}
		err = c.Actions.PutArtifactBlock(a, blockId, body)
	case "blocklist":
		var list []byte
		list, err = io.ReadAll(io.LimitReader(body, 8<<20))
		if err == nil {
			err = c.Actions.CommitArtifactBlocks(a, list)
		}
	case "":
		err = c.Actions.PutArtifactBlock(a, "", body)
	default:
		return c.fail(ctx, application.ErrInvalidBlockList)
	}
	if err != nil {
		if errors.Is(err, application.ErrArtifactTooLarge) {
			// The client reads the answer once it has sent the body.
			_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<20))
		}
		return c.fail(ctx, err)
	}
	ctx.Set("x-ms-request-id", a.Id)
	return ctx.SendStatus(fiber.StatusCreated)
}

// Download streams the zip of a signed URL.
func (c Controller) Download(ctx *fiber.Ctx) error {
	a, err := c.Actions.VerifyArtifactURL("DownloadArtifact", ctx.Query("artifact"), ctx.Query("expires"), ctx.Query("sig"))
	if err != nil {
		return c.fail(ctx, err)
	}
	r, err := c.Actions.OpenArtifact(a)
	if err != nil {
		return c.fail(ctx, err)
	}
	ctx.Set(fiber.HeaderContentType, "application/zip")
	ctx.Set(fiber.HeaderContentDisposition, `attachment; filename="`+strings.ReplaceAll(a.Name, `"`, "")+`.zip"`)
	ctx.Set("X-Content-Type-Options", "nosniff")
	return ctx.SendStream(r, int(a.Size))
}
