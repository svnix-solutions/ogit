package actions

import (
	"mime"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type Artifact struct {
	Id          int64     `json:"id"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	DownloadURL string    `json:"download_url"` // the zip, for readers
}

type ArtifactsResponse struct {
	Artifacts []Artifact `json:"artifacts"`
}

type ArtifactRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	ArtifactId string `path:"artifact_id" json:"-" validate:"required"`
}

func setupArtifacts(c Controller, repoGrp *fiberoapi.OApiGroup, app *fiber.App) {
	tags := []string{"Actions"}
	fiberoapi.Get(repoGrp, "/:identifier/actions/runs/:number/artifacts", c.RunArtifacts, fiberoapi.OpenAPIOptions{Summary: "List a run's artifacts",
		OperationID: "actions.artifacts", Tags: tags, Security: "disabled", Description: "Uploaded with actions/upload-artifact, until they expire. No session needed for public repositories."})
	fiberoapi.Delete(repoGrp, "/:identifier/actions/artifacts/:artifact_id", c.DeleteArtifact, fiberoapi.OpenAPIOptions{Summary: "Delete an artifact",
		OperationID: "actions.artifact.delete", Tags: tags, Description: "Writers."})
	// A download, not a JSON operation.
	app.Get("/api/v1/repository/:identifier/actions/artifacts/:artifact_id/zip", c.DownloadArtifact)
}

func (c Controller) RunArtifacts(ctx *fiber.Ctx, req RunRequest) (*ArtifactsResponse, *fiberoapi.ErrorResponse) {
	n, e := number(req.Number)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	list, err := c.Actions.ListRunArtifacts(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), n)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ArtifactsResponse{Artifacts: make([]Artifact, 0, len(list))}
	for _, a := range list {
		resp.Artifacts = append(resp.Artifacts, Artifact{Id: a.ApiId, Name: a.Name, Size: a.Size, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt,
			DownloadURL: "/api/v1/repository/" + req.Identifier + "/actions/artifacts/" + strconv.FormatInt(a.ApiId, 10) + "/zip"})
	}
	return resp, nil
}

func (c Controller) DeleteArtifact(ctx *fiber.Ctx, req ArtifactRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	user, e := authed(ctx)
	if e != nil {
		return nil, e
	}
	apiId, err := strconv.ParseInt(req.ArtifactId, 10, 64)
	if err != nil {
		return nil, &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: "artifact not found", Type: "ARTIFACT_NOT_FOUND"}
	}
	id, path := repoid.Split(req.Identifier)
	if err := c.Actions.DeleteArtifact(id, path, user, apiId); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "artifact deleted"}, nil
}

// DownloadArtifact sends an artifact's zip to a reader.
func (c Controller) DownloadArtifact(ctx *fiber.Ctx) error {
	fail := func(e *fiberoapi.ErrorResponse) error { return ctx.Status(e.Code).JSON(e) }
	apiId, err := strconv.ParseInt(ctx.Params("artifact_id"), 10, 64)
	if err != nil {
		return fail(&fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: "artifact not found", Type: "ARTIFACT_NOT_FOUND"})
	}
	id, path := repoid.Split(ctx.Params("identifier"))
	_, a, err := c.Actions.ReadableArtifact(id, path, optauth.Reader(ctx, c.SessionApp, c.TokenApp), apiId)
	if err != nil {
		return fail(apierror.From(err, c.Logger))
	}
	r, err := c.Actions.OpenArtifact(a)
	if err != nil {
		return fail(apierror.From(err, c.Logger))
	}
	ctx.Set(fiber.HeaderContentType, "application/zip")
	ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": a.Name + ".zip"}))
	ctx.Set("X-Content-Type-Options", "nosniff")
	ctx.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctx.Set(fiber.HeaderCacheControl, "private, no-cache")
	return ctx.SendStream(r, int(a.Size))
}
