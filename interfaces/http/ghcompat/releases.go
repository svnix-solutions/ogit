package ghcompat

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/releases"
)

// GitHub's releases API, enough for release tools (goreleaser and the
// like, through go-github): ids are the numeric api_id, since clients
// parse them as integers; users have a login and no id for the same
// reason. Uploads are also answered under /api/uploads, GitHub
// Enterprise's upload URL.

type ghReleaseUser struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type ghAsset struct {
	URL                string         `json:"url"`
	BrowserDownloadURL string         `json:"browser_download_url"`
	Id                 int64          `json:"id"`
	NodeId             string         `json:"node_id"`
	Name               string         `json:"name"`
	Label              string         `json:"label"`
	State              string         `json:"state"`
	ContentType        string         `json:"content_type"`
	Size               int64          `json:"size"`
	DownloadCount      int64          `json:"download_count"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
	Uploader           *ghReleaseUser `json:"uploader"`
}

type ghRelease struct {
	URL             string         `json:"url"`
	HTMLURL         string         `json:"html_url"`
	AssetsURL       string         `json:"assets_url"`
	UploadURL       string         `json:"upload_url"`
	TarballURL      string         `json:"tarball_url"`
	ZipballURL      string         `json:"zipball_url"`
	Id              int64          `json:"id"`
	NodeId          string         `json:"node_id"`
	TagName         string         `json:"tag_name"`
	TargetCommitish string         `json:"target_commitish"`
	Name            string         `json:"name"`
	Body            string         `json:"body"`
	Draft           bool           `json:"draft"`
	Prerelease      bool           `json:"prerelease"`
	CreatedAt       time.Time      `json:"created_at"`
	PublishedAt     *time.Time     `json:"published_at"`
	Author          *ghReleaseUser `json:"author"`
	Assets          []ghAsset      `json:"assets"`
}

type ghReleaseInput struct {
	TagName              *string `json:"tag_name"`
	TargetCommitish      *string `json:"target_commitish"`
	Name                 *string `json:"name"`
	Body                 *string `json:"body"`
	Draft                *bool   `json:"draft"`
	Prerelease           *bool   `json:"prerelease"`
	GenerateReleaseNotes bool    `json:"generate_release_notes"`
}

func setupReleases(c Controller, app *fiber.App) {
	g := app.Group("/api/v3/repos/:owner/:repo/releases")
	g.Get("/", c.listReleases)
	g.Post("/", c.createRelease)
	g.Get("/latest", c.latestRelease)
	g.Get("/tags/*", c.releaseByTag)
	g.Post("/generate-notes", c.generateNotes)
	g.Get("/assets/:asset_id", c.getAsset)
	g.Patch("/assets/:asset_id", c.updateAsset)
	g.Delete("/assets/:asset_id", c.deleteAsset)
	g.Get("/:id", c.getRelease)
	g.Patch("/:id", c.updateRelease)
	g.Delete("/:id", c.deleteRelease)
	g.Get("/:id/assets", c.listAssets)
	g.Post("/:id/assets", c.uploadAsset)
	app.Post("/api/uploads/repos/:owner/:repo/releases/:id/assets", c.uploadAsset)
}

func (c Controller) root() string { return strings.TrimRight(c.Config.Server.PublicURL, "/") }

func releaseUser(login string) *ghReleaseUser {
	return &ghReleaseUser{Login: login, Type: "User"}
}

func (c Controller) asset(a *application.AssetView) ghAsset {
	api := c.base() + "/repos/" + a.Repository.FullPath() + "/releases/assets/" + strconv.FormatInt(a.ApiId, 10)
	out := ghAsset{URL: api, Id: a.ApiId, NodeId: a.Id, Name: a.Name, Label: a.Label, State: "uploaded", ContentType: a.ContentType,
		Size: a.Size, DownloadCount: a.DownloadCount, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
	if !a.Release.Draft {
		out.BrowserDownloadURL = application.AssetDownloadURL(c.root(), a.Repository, a.Release.TagName, a.Name)
	}
	if a.Uploader != nil {
		out.Uploader = releaseUser(a.Uploader.Username)
	}
	return out
}

func (c Controller) release(v *application.ReleaseView) ghRelease {
	id := strconv.FormatInt(v.ApiId, 10)
	repoPath := v.Repository.FullPath()
	api := c.base() + "/repos/" + repoPath + "/releases/" + id
	html := c.root() + "/" + repoPath
	archive := html + "/archive/" + url.PathEscape(v.TagName)
	out := ghRelease{URL: api, HTMLURL: html + "/releases/tag/" + url.PathEscape(v.TagName), AssetsURL: api + "/assets",
		UploadURL:  c.root() + "/api/uploads/repos/" + repoPath + "/releases/" + id + "/assets{?name,label}",
		TarballURL: archive + ".tar.gz", ZipballURL: archive + ".zip", Id: v.ApiId, NodeId: v.Id, TagName: v.TagName,
		TargetCommitish: v.TargetCommitish, Name: v.Name, Body: v.Body, Draft: v.Draft, Prerelease: v.Prerelease,
		CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt, Assets: make([]ghAsset, 0, len(v.Assets))}
	if v.Author != nil {
		out.Author = releaseUser(v.Author.Username)
	}
	for i := range v.Assets {
		out.Assets = append(out.Assets, c.asset(&application.AssetView{ReleaseAsset: &v.Assets[i], Release: v.Release, Repository: v.Repository}))
	}
	return out
}

// failRelease answers validation errors with GitHub's 422.
func (c Controller) failRelease(ctx *fiber.Ctx, err error) error {
	for _, e := range []error{application.ErrReleaseExists, application.ErrInvalidRelease, application.ErrAssetExists, application.ErrInvalidAsset} {
		if errors.Is(err, e) {
			return c.fail(ctx, fiber.StatusUnprocessableEntity, "Validation Failed: "+err.Error())
		}
	}
	return c.failWith(ctx, err)
}

// decode reads a JSON body whatever its Content-Type (curl -d sends
// form-urlencoded), as GitHub does; an empty body is an empty object.
func decode(ctx *fiber.Ctx, v any) error {
	if len(bytes.TrimSpace(ctx.Body())) == 0 {
		return nil
	}
	return json.Unmarshal(ctx.Body(), v)
}

func apiId(ctx *fiber.Ctx, param string) (int64, bool) {
	n, err := strconv.ParseInt(ctx.Params(param), 10, 64)
	return n, err == nil && n > 0
}

// writer returns the caller, refusing anonymous ones like GitHub.
func (c Controller) writer(ctx *fiber.Ctx) (application.StatusCaller, *string, error) {
	caller, path, err := c.caller(ctx)
	if err == nil && caller.UserId == "" {
		err = errUnauthenticated
	}
	return caller, path, err
}

var errUnauthenticated = errors.New("Requires authentication")

func (c Controller) failCaller(ctx *fiber.Ctx, err error) error {
	if errors.Is(err, errUnauthenticated) {
		return c.fail(ctx, fiber.StatusUnauthorized, err.Error())
	}
	return c.failWith(ctx, err)
}

// byId finds the release of the :id parameter.
func (c Controller) byId(ctx *fiber.Ctx, caller application.StatusCaller, path *string) (*application.ReleaseView, error) {
	n, ok := apiId(ctx, "id")
	if !ok {
		return nil, application.ErrReleaseNotFound
	}
	return c.Releases.ByApiId(nil, path, caller, n)
}

func (c Controller) listReleases(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	perPage := min(max(ctx.QueryInt("per_page", 30), 1), 100)
	page := max(ctx.QueryInt("page", 1), 1)
	views, _, err := c.Releases.List(nil, path, caller, (page-1)*perPage, perPage)
	if err != nil {
		return c.failWith(ctx, err)
	}
	out := make([]ghRelease, 0, len(views))
	for i := range views {
		out = append(out, c.release(&views[i]))
	}
	return ctx.JSON(out)
}

func (c Controller) sendRelease(ctx *fiber.Ctx, status int, v *application.ReleaseView, err error) error {
	if err != nil {
		return c.failRelease(ctx, err)
	}
	return ctx.Status(status).JSON(c.release(v))
}

func (c Controller) getRelease(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	v, err := c.byId(ctx, caller, path)
	return c.sendRelease(ctx, fiber.StatusOK, v, err)
}

func (c Controller) latestRelease(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	v, err := c.Releases.Latest(nil, path, caller)
	return c.sendRelease(ctx, fiber.StatusOK, v, err)
}

func (c Controller) releaseByTag(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	tag, err := url.PathUnescape(ctx.Params("*"))
	if err != nil {
		return c.fail(ctx, fiber.StatusNotFound, "Not Found")
	}
	v, err := c.Releases.ByTag(nil, path, caller, tag)
	return c.sendRelease(ctx, fiber.StatusOK, v, err)
}

func (in ghReleaseInput) release() application.ReleaseInput {
	return application.ReleaseInput{TagName: in.TagName, TargetCommitish: in.TargetCommitish, Name: in.Name, Body: in.Body,
		Draft: in.Draft, Prerelease: in.Prerelease}
}

func (c Controller) createRelease(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	var in ghReleaseInput
	if err := decode(ctx, &in); err != nil {
		return c.fail(ctx, fiber.StatusBadRequest, "Problems parsing JSON")
	}
	if in.GenerateReleaseNotes && in.TagName != nil {
		target := ""
		if in.TargetCommitish != nil {
			target = *in.TargetCommitish
		}
		notes, err := c.Releases.GenerateNotes(nil, path, caller, *in.TagName, target, "")
		if err != nil {
			return c.failRelease(ctx, err)
		}
		// Like GitHub: a given body comes first, a given name wins.
		body := notes.Body
		if in.Body != nil && *in.Body != "" {
			body = *in.Body + "\n\n" + notes.Body
		}
		in.Body = &body
		if in.Name == nil || *in.Name == "" {
			in.Name = &notes.Name
		}
	}
	v, err := c.Releases.Create(nil, path, caller, in.release())
	return c.sendRelease(ctx, fiber.StatusCreated, v, err)
}

func (c Controller) updateRelease(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	v, err := c.byId(ctx, caller, path)
	if err != nil {
		return c.failWith(ctx, err)
	}
	var in ghReleaseInput
	if err := decode(ctx, &in); err != nil {
		return c.fail(ctx, fiber.StatusBadRequest, "Problems parsing JSON")
	}
	v, err = c.Releases.Update(&v.Repository.Id, nil, caller, v.Id, in.release())
	return c.sendRelease(ctx, fiber.StatusOK, v, err)
}

func (c Controller) deleteRelease(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	v, err := c.byId(ctx, caller, path)
	if err == nil {
		err = c.Releases.Delete(&v.Repository.Id, nil, caller, v.Id)
	}
	if err != nil {
		return c.failWith(ctx, err)
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}

func (c Controller) generateNotes(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	var in struct {
		TagName         string `json:"tag_name"`
		TargetCommitish string `json:"target_commitish"`
		PreviousTagName string `json:"previous_tag_name"`
	}
	if err := decode(ctx, &in); err != nil || in.TagName == "" {
		return c.fail(ctx, fiber.StatusUnprocessableEntity, "Validation Failed: tag_name is required")
	}
	notes, err := c.Releases.GenerateNotes(nil, path, caller, in.TagName, in.TargetCommitish, in.PreviousTagName)
	if err != nil {
		return c.failRelease(ctx, err)
	}
	return ctx.JSON(fiber.Map{"name": notes.Name, "body": notes.Body})
}

func (c Controller) listAssets(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	v, err := c.byId(ctx, caller, path)
	if err != nil {
		return c.failWith(ctx, err)
	}
	return ctx.JSON(c.release(v).Assets)
}

func (c Controller) uploadAsset(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	v, err := c.byId(ctx, caller, path)
	if err != nil {
		return c.failWith(ctx, err)
	}
	a, err := c.Releases.UploadAsset(&v.Repository.Id, nil, caller, v.Id, releases.RequestUpload(ctx))
	if err != nil {
		return c.failRelease(ctx, err)
	}
	return ctx.Status(fiber.StatusCreated).JSON(c.asset(a))
}

func (c Controller) assetById(ctx *fiber.Ctx, caller application.StatusCaller, path *string) (*application.AssetView, error) {
	n, ok := apiId(ctx, "asset_id")
	if !ok {
		return nil, application.ErrAssetNotFound
	}
	return c.Releases.AssetByApiId(nil, path, caller, n)
}

// getAsset answers the asset, or its file with Accept: application/octet-stream.
func (c Controller) getAsset(ctx *fiber.Ctx) error {
	caller, path, err := c.caller(ctx)
	if err != nil {
		return c.failWith(ctx, err)
	}
	a, err := c.assetById(ctx, caller, path)
	if err != nil {
		return c.failWith(ctx, err)
	}
	if strings.Contains(ctx.Get(fiber.HeaderAccept), "application/octet-stream") {
		if err := releases.SendAsset(ctx, c.Releases, a); err != nil {
			return c.failWith(ctx, err)
		}
		return nil
	}
	return ctx.JSON(c.asset(a))
}

func (c Controller) updateAsset(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	a, err := c.assetById(ctx, caller, path)
	if err != nil {
		return c.failWith(ctx, err)
	}
	var in struct {
		Name  *string `json:"name"`
		Label *string `json:"label"`
	}
	if err := decode(ctx, &in); err != nil {
		return c.fail(ctx, fiber.StatusBadRequest, "Problems parsing JSON")
	}
	a, err = c.Releases.UpdateAsset(&a.Repository.Id, nil, caller, a.Id, application.AssetChange{Name: in.Name, Label: in.Label})
	if err != nil {
		return c.failRelease(ctx, err)
	}
	return ctx.JSON(c.asset(a))
}

func (c Controller) deleteAsset(ctx *fiber.Ctx) error {
	caller, path, err := c.writer(ctx)
	if err != nil {
		return c.failCaller(ctx, err)
	}
	a, err := c.assetById(ctx, caller, path)
	if err == nil {
		err = c.Releases.DeleteAsset(&a.Repository.Id, nil, caller, a.Id)
	}
	if err != nil {
		return c.failWith(ctx, err)
	}
	return ctx.SendStatus(fiber.StatusNoContent)
}
