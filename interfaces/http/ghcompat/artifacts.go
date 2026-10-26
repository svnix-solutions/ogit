package ghcompat

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/artifacts"
)

// GitHub's artifacts REST API, as download-artifact v4 uses it with
// run-id and github-token: list a run's artifacts, and a redirect to an
// artifact's zip (a signed URL, fetched without credentials).

type ghArtifact struct {
	Id                 int64          `json:"id"`
	NodeId             string         `json:"node_id"`
	Name               string         `json:"name"`
	SizeInBytes        int64          `json:"size_in_bytes"`
	URL                string         `json:"url"`
	ArchiveDownloadURL string         `json:"archive_download_url"`
	Expired            bool           `json:"expired"`
	CreatedAt          time.Time      `json:"created_at"`
	ExpiresAt          time.Time      `json:"expires_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	WorkflowRun        map[string]any `json:"workflow_run"`
}

func setupArtifacts(c Controller, app *fiber.App) {
	g := app.Group("/api/v3/repos/:owner/:repo/actions")
	g.Get("/runs/:run_id/artifacts", c.runArtifacts)
	g.Get("/artifacts/:artifact_id", c.getArtifact)
	g.Get("/artifacts/:artifact_id/zip", c.artifactZip)
}

func (c Controller) ghArtifact(path string, run *domain.ActionRun, a domain.ActionArtifact) ghArtifact {
	url := c.base() + "/repos/" + path + "/actions/artifacts/" + strconv.FormatInt(a.ApiId, 10)
	return ghArtifact{Id: a.ApiId, NodeId: a.Id, Name: a.Name, SizeInBytes: a.Size, URL: url, ArchiveDownloadURL: url + "/zip",
		CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, UpdatedAt: a.CreatedAt,
		WorkflowRun: map[string]any{"id": run.ApiId, "head_sha": run.Sha}}
}

func (c Controller) runArtifacts(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	runId, err := strconv.ParseInt(ctx.Params("run_id"), 10, 64)
	if err != nil {
		return c.failWith(ctx, application.ErrRunNotFound)
	}
	_, run, err := c.Actions.RunByApiId(nil, path, caller.UserId, runId)
	if err != nil {
		return c.failWith(ctx, err)
	}
	list, err := c.Actions.RunArtifacts(run, ctx.Query("name"))
	if err != nil {
		return c.failWith(ctx, err)
	}
	out := make([]ghArtifact, 0, len(list))
	for _, a := range list {
		if time.Now().Before(a.ExpiresAt) {
			out = append(out, c.ghArtifact(*path, run, a))
		}
	}
	return ctx.JSON(fiber.Map{"total_count": len(out), "artifacts": out})
}

func (c Controller) artifact(ctx *fiber.Ctx) (*domain.ActionArtifact, *domain.ActionRun, *string, error) {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	apiId, err := strconv.ParseInt(ctx.Params("artifact_id"), 10, 64)
	if err != nil {
		return nil, nil, nil, application.ErrArtifactNotFound
	}
	_, a, err := c.Actions.ReadableArtifact(nil, path, caller.UserId, apiId)
	if err != nil {
		return nil, nil, nil, err
	}
	run, err := c.Actions.Pers.GetRunById(a.RunId)
	if err != nil {
		return nil, nil, nil, application.ErrArtifactNotFound
	}
	return a, run, path, nil
}

func (c Controller) getArtifact(ctx *fiber.Ctx) error {
	a, run, path, err := c.artifact(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	return ctx.JSON(c.ghArtifact(*path, run, *a))
}

// artifactZip redirects to a signed download URL, like GitHub.
func (c Controller) artifactZip(ctx *fiber.Ctx) error {
	a, _, _, err := c.artifact(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	base := c.root()
	if base == "" {
		base = ctx.BaseURL()
	}
	return ctx.Redirect(base+artifacts.Base+"DownloadArtifact?"+c.Actions.SignArtifactURL(a, "DownloadArtifact"), fiber.StatusFound)
}
