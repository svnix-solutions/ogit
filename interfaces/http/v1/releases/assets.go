package releases

import (
	"bytes"
	"mime"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
)

type Asset struct {
	Id            string    `json:"id"`
	Name          string    `json:"name"`
	Label         string    `json:"label"`
	ContentType   string    `json:"content_type"`
	Size          int64     `json:"size"`
	DownloadCount int64     `json:"download_count"`
	Uploader      string    `json:"uploader,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	// URL downloads through the API (session or token); BrowserDownloadURL
	// is the public link, for published releases only.
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url,omitempty"`
}

type AssetRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	ReleaseId  string `path:"release_id" json:"-" validate:"required"`
	AssetId    string `path:"asset_id" json:"-" validate:"required"`
}

type UpdateAssetRequest struct {
	Identifier string  `path:"identifier" json:"-" validate:"required"`
	ReleaseId  string  `path:"release_id" json:"-" validate:"required"`
	AssetId    string  `path:"asset_id" json:"-" validate:"required"`
	Name       *string `json:"name"`
	Label      *string `json:"label"`
}

type ListAssetsResponse struct {
	Assets []Asset `json:"assets"`
}

func setupAssets(c Controller, repoGrp *fiberoapi.OApiGroup, opts func(summary, id, desc string) fiberoapi.OpenAPIOptions) {
	fiberoapi.Get(repoGrp, "/:identifier/releases/:release_id/assets", c.ListAssets, opts("List a release's assets", "releases.assets.list",
		"Upload with POST to this path, the file as the body: ?name=<file name>&label=<optional>, Content-Type, at most releases.max_asset_size bytes (writers)."))
	fiberoapi.Get(repoGrp, "/:identifier/releases/:release_id/assets/:asset_id", c.GetAsset, opts("Get a release asset", "releases.assets.get",
		"The file itself is at .../download (counted)."))
	fiberoapi.Patch(repoGrp, "/:identifier/releases/:release_id/assets/:asset_id", c.UpdateAsset, opts("Rename a release asset", "releases.assets.update", "Writers. name, label."))
	fiberoapi.Delete(repoGrp, "/:identifier/releases/:release_id/assets/:asset_id", c.DeleteAsset, opts("Delete a release asset", "releases.assets.delete", "Writers."))
}

// SetupFiles registers the upload and download routes: plain routes, since
// the bodies are files, not JSON. Uploads are streamed (see
// interfaces/http.bodyLimit).
func SetupFiles(c Controller, app *fiber.App) {
	app.Post("/api/v1/repository/:identifier/releases/:release_id/assets", c.Upload)
	app.Get("/api/v1/repository/:identifier/releases/:release_id/assets/:asset_id/download", c.Download)
}

func (c Controller) base() string { return strings.TrimRight(c.PublicURL, "/") }

func (c Controller) toAsset(a *application.AssetView) Asset {
	out := Asset{Id: a.Id, Name: a.Name, Label: a.Label, ContentType: a.ContentType, Size: a.Size, DownloadCount: a.DownloadCount,
		CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		URL: c.base() + "/api/v1/repository/" + a.Repository.Id + "/releases/" + a.ReleaseId + "/assets/" + a.Id + "/download"}
	if a.Uploader != nil {
		out.Uploader = a.Uploader.Username
	}
	if !a.Release.Draft {
		out.BrowserDownloadURL = application.AssetDownloadURL(c.base(), a.Repository, a.Release.TagName, a.Name)
	}
	return out
}

func (c Controller) assets(v *application.ReleaseView) []Asset {
	out := make([]Asset, 0, len(v.Assets))
	for i := range v.Assets {
		out = append(out, c.toAsset(&application.AssetView{ReleaseAsset: &v.Assets[i], Release: v.Release, Repository: v.Repository}))
	}
	return out
}

func (c Controller) ListAssets(ctx *fiber.Ctx, req IdRequest) (*ListAssetsResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	v, err := c.Releases.Get(id, path, caller, req.ReleaseId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &ListAssetsResponse{Assets: c.assets(v)}, nil
}

// asset finds an asset of the release in the path.
func (c Controller) asset(caller application.StatusCaller, identifier, releaseId, assetId string) (*application.AssetView, error) {
	id, path := repoid.Split(identifier)
	a, err := c.Releases.GetAsset(id, path, caller, assetId)
	if err != nil {
		return nil, err
	}
	if a.ReleaseId != releaseId {
		return nil, application.ErrAssetNotFound
	}
	return a, nil
}

func (c Controller) GetAsset(ctx *fiber.Ctx, req AssetRequest) (*Asset, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	a, err := c.asset(caller, req.Identifier, req.ReleaseId, req.AssetId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := c.toAsset(a)
	return &out, nil
}

func (c Controller) UpdateAsset(ctx *fiber.Ctx, req UpdateAssetRequest) (*Asset, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	a, err := c.asset(caller, req.Identifier, req.ReleaseId, req.AssetId)
	if err == nil {
		a, err = c.Releases.UpdateAsset(&a.Repository.Id, nil, caller, a.Id, application.AssetChange{Name: req.Name, Label: req.Label})
	}
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	out := c.toAsset(a)
	return &out, nil
}

func (c Controller) DeleteAsset(ctx *fiber.Ctx, req AssetRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	caller, e := c.caller(ctx)
	if e != nil {
		return nil, e
	}
	a, err := c.asset(caller, req.Identifier, req.ReleaseId, req.AssetId)
	if err == nil {
		err = c.Releases.DeleteAsset(&a.Repository.Id, nil, caller, a.Id)
	}
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "asset deleted"}, nil
}

func (c Controller) failJSON(ctx *fiber.Ctx, e *fiberoapi.ErrorResponse) error {
	return ctx.Status(e.Code).JSON(e)
}

// RequestUpload reads an upload's name, label, type and body (streamed
// when large).
func RequestUpload(ctx *fiber.Ctx) application.AssetUpload {
	up := application.AssetUpload{Name: ctx.Query("name"), Label: ctx.Query("label"), ContentType: ctx.Get(fiber.HeaderContentType), Size: -1}
	if n := ctx.Request().Header.ContentLength(); n >= 0 {
		up.Size = int64(n)
	}
	if ctx.Request().IsBodyStream() {
		up.Body = ctx.Request().BodyStream()
	} else {
		up.Body = bytes.NewReader(ctx.Body())
	}
	return up
}

func (c Controller) Upload(ctx *fiber.Ctx) error {
	caller, e := c.caller(ctx)
	if e != nil {
		return c.failJSON(ctx, e)
	}
	id, path := repoid.Split(ctx.Params("identifier"))
	a, err := c.Releases.UploadAsset(id, path, caller, ctx.Params("release_id"), RequestUpload(ctx))
	if err != nil {
		return c.failJSON(ctx, apierror.From(err, c.Logger))
	}
	return ctx.JSON(c.toAsset(a))
}

func (c Controller) Download(ctx *fiber.Ctx) error {
	caller, e := c.caller(ctx)
	if e != nil {
		return c.failJSON(ctx, e)
	}
	a, err := c.asset(caller, ctx.Params("identifier"), ctx.Params("release_id"), ctx.Params("asset_id"))
	if err != nil {
		return c.failJSON(ctx, apierror.From(err, c.Logger))
	}
	if err := SendAsset(ctx, c.Releases, a); err != nil {
		return c.failJSON(ctx, apierror.From(err, c.Logger))
	}
	return nil
}

// SendAsset streams an asset as a download. Its declared type is never
// used: a file uploaded as HTML must not run on this site's origin.
func SendAsset(ctx *fiber.Ctx, releases *application.ReleaseApp, a *application.AssetView) error {
	f, err := releases.OpenAsset(a)
	if err != nil {
		return err
	}
	ctx.Set(fiber.HeaderContentType, "application/octet-stream")
	ctx.Set(fiber.HeaderContentDisposition, mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	ctx.Set("X-Content-Type-Options", "nosniff")
	ctx.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	ctx.Set(fiber.HeaderCacheControl, "private, no-cache")
	// fasthttp closes f once the body is sent.
	return ctx.SendStream(f, int(a.Size))
}

// SplitDownload splits "<tag>/<name>" (the tag may contain slashes).
func SplitDownload(rest string) (tag, name string, ok bool) {
	i := strings.LastIndex(rest, "/")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	tag, err1 := url.PathUnescape(rest[:i])
	name, err2 := url.PathUnescape(rest[i+1:])
	return tag, name, err1 == nil && err2 == nil
}
