package application

import (
	"errors"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Release asset rules:
//   - writers upload, rename and delete assets, on drafts and published
//     releases alike; anyone who can see the release downloads them;
//   - names are made safe (domain.AssetName) and unique per release;
//   - uploads are streamed to storage and refused past
//     releases.max_asset_size (100 MB by default): the file is deleted;
//   - files live with the repository's data, so deleting the repository
//     deletes them; deleting a release deletes its assets;
//   - every download counts.

var (
	ErrAssetNotFound = errors.New("release asset not found")
	ErrAssetExists   = errors.New("this release already has an asset with this name")
	ErrAssetTooLarge = errors.New("the asset is larger than the limit (releases.max_asset_size)")
	ErrInvalidAsset  = errors.New("an asset needs a file name (letters, digits, . _ - + @ ~) and a label of at most 255 characters")
)

// DefaultMaxAssetSize applies when releases.max_asset_size is 0.
const DefaultMaxAssetSize = 100 << 20

// AssetView is an asset with its release and repository.
type AssetView struct {
	*domain.ReleaseAsset
	Release    *domain.Release
	Repository *domain.Repository
}

func (c *ReleaseApp) maxAssetSize() int64 {
	if c.MaxAssetSize > 0 {
		return c.MaxAssetSize
	}
	return DefaultMaxAssetSize
}

// MaxAssetBytes is the upload limit (for handlers that check
// Content-Length first).
func (c *ReleaseApp) MaxAssetBytes() int64 { return c.maxAssetSize() }

// withAssets fills the views' assets in one query.
func (c *ReleaseApp) withAssets(views []ReleaseView) error {
	if c.Assets == nil || len(views) == 0 {
		return nil
	}
	ids := make([]string, 0, len(views))
	for _, v := range views {
		ids = append(ids, v.Id)
	}
	assets, err := c.Assets.List(ids...)
	if err != nil {
		return err
	}
	for i := range views {
		views[i].Assets = []domain.ReleaseAsset{}
		for _, a := range assets {
			if a.ReleaseId == views[i].Id {
				views[i].Assets = append(views[i].Assets, a)
			}
		}
	}
	return nil
}

// asset returns an asset of a release the caller can see.
func (c *ReleaseApp) asset(id, path *string, caller StatusCaller, lookup func(repoId string) (*domain.ReleaseAsset, error)) (*AssetView, bool, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, false, err
	}
	a, err := lookup(repo.Id)
	if isNotFound(err) {
		return nil, false, ErrAssetNotFound
	} else if err != nil {
		return nil, false, err
	}
	r, err := c.Releases.Get(repo.Id, a.ReleaseId)
	if _, err := c.view(repo, writer, r, err); errors.Is(err, ErrReleaseNotFound) {
		return nil, false, ErrAssetNotFound
	} else if err != nil {
		return nil, false, err
	}
	return &AssetView{ReleaseAsset: a, Release: r, Repository: repo}, writer, nil
}

func (c *ReleaseApp) GetAsset(id, path *string, caller StatusCaller, assetId string) (*AssetView, error) {
	v, _, err := c.asset(id, path, caller, func(repoId string) (*domain.ReleaseAsset, error) { return c.Assets.Get(repoId, assetId) })
	return v, err
}

// AssetByApiId: GetAsset by the GitHub-compatible numeric id.
func (c *ReleaseApp) AssetByApiId(id, path *string, caller StatusCaller, apiId int64) (*AssetView, error) {
	v, _, err := c.asset(id, path, caller, func(repoId string) (*domain.ReleaseAsset, error) { return c.Assets.ByApiId(repoId, apiId) })
	return v, err
}

// AssetUpload describes an uploaded file; Size is -1 when unknown.
type AssetUpload struct {
	Name        string
	Label       string
	ContentType string
	Size        int64
	Body        io.Reader
}

// countingReader counts what was read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

func (c *ReleaseApp) UploadAsset(id, path *string, caller StatusCaller, releaseId string, up AssetUpload) (*AssetView, error) {
	rel, err := c.Get(id, path, caller, releaseId)
	if err != nil {
		return nil, err
	}
	if !rel.CanEdit {
		return nil, ErrForbidden
	}
	name, label := domain.AssetName(up.Name), strings.TrimSpace(up.Label)
	if name == "" || len(label) > 255 {
		return nil, ErrInvalidAsset
	}
	limit := c.maxAssetSize()
	if up.Size > limit {
		return nil, ErrAssetTooLarge
	}
	if _, err := c.Assets.ByName(rel.Id, name); err == nil {
		return nil, ErrAssetExists
	}
	contentType := "application/octet-stream"
	if mt, params, err := mime.ParseMediaType(up.ContentType); err == nil {
		contentType = mime.FormatMediaType(mt, params)
	}
	a := &domain.ReleaseAsset{Id: utils.UUIDv4(), ReleaseId: rel.Id, RepositoryId: rel.Repository.Id, Name: name, Label: label, ContentType: contentType}
	if domain.IsPerson(caller.UserId) {
		a.UploaderId = &caller.UserId
	}
	storage := c.Repositories.StorageConfig
	body := &countingReader{r: io.LimitReader(up.Body, limit+1)}
	if err := storage.PutAsset(rel.Repository.StorageKey, a.Id, body); err != nil {
		_ = storage.DeleteAsset(rel.Repository.StorageKey, a.Id)
		return nil, err
	}
	discard := func(err error) (*AssetView, error) {
		if derr := storage.DeleteAsset(rel.Repository.StorageKey, a.Id); derr != nil {
			c.Logger.Warn().Err(derr).Str("asset", a.Id).Msg("could not delete a refused release asset")
		}
		return nil, err
	}
	switch {
	case body.n > limit:
		return discard(ErrAssetTooLarge)
	case up.Size >= 0 && body.n != up.Size:
		return discard(ErrInvalidAsset) // shorter than announced
	}
	a.Size = body.n
	if err := c.Assets.Create(a); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return discard(ErrAssetExists)
	} else if err != nil {
		return discard(err)
	}
	return c.GetAsset(&rel.Repository.Id, nil, caller, a.Id)
}

// AssetChange renames or relabels an asset; nil fields are kept.
type AssetChange struct {
	Name  *string
	Label *string
}

func (c *ReleaseApp) UpdateAsset(id, path *string, caller StatusCaller, assetId string, ch AssetChange) (*AssetView, error) {
	v, writer, err := c.asset(id, path, caller, func(repoId string) (*domain.ReleaseAsset, error) { return c.Assets.Get(repoId, assetId) })
	if err != nil {
		return nil, err
	}
	if !writer {
		return nil, ErrForbidden
	}
	fields := map[string]any{"updated_at": time.Now()}
	if ch.Name != nil {
		name := domain.AssetName(*ch.Name)
		if name == "" {
			return nil, ErrInvalidAsset
		}
		if other, err := c.Assets.ByName(v.ReleaseId, name); err == nil && other.Id != v.Id {
			return nil, ErrAssetExists
		}
		fields["name"] = name
	}
	if ch.Label != nil {
		label := strings.TrimSpace(*ch.Label)
		if len(label) > 255 {
			return nil, ErrInvalidAsset
		}
		fields["label"] = label
	}
	if err := c.Assets.Update(v.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrAssetExists
	} else if err != nil {
		return nil, err
	}
	return c.GetAsset(&v.Repository.Id, nil, caller, v.Id)
}

func (c *ReleaseApp) DeleteAsset(id, path *string, caller StatusCaller, assetId string) error {
	v, writer, err := c.asset(id, path, caller, func(repoId string) (*domain.ReleaseAsset, error) { return c.Assets.Get(repoId, assetId) })
	if err != nil {
		return err
	}
	if !writer {
		return ErrForbidden
	}
	if err := c.Assets.Delete(v.Id); err != nil {
		return err
	}
	c.deleteFiles(v.Repository, []domain.ReleaseAsset{*v.ReleaseAsset})
	return nil
}

func (c *ReleaseApp) deleteFiles(repo *domain.Repository, assets []domain.ReleaseAsset) {
	for _, a := range assets {
		if err := c.Repositories.StorageConfig.DeleteAsset(repo.StorageKey, a.Id); err != nil {
			c.Logger.Warn().Err(err).Str("asset", a.Id).Msg("could not delete a release asset's file")
		}
	}
}

// OpenAsset returns an asset's file for download and counts it.
func (c *ReleaseApp) OpenAsset(v *AssetView) (io.ReadCloser, error) {
	f, err := c.Repositories.StorageConfig.OpenAsset(v.Repository.StorageKey, v.Id)
	if err != nil {
		return nil, err
	}
	if err := c.Assets.CountDownload(v.Id); err != nil {
		c.Logger.Warn().Err(err).Str("asset", v.Id).Msg("could not count a download")
	}
	return f, nil
}

// PublishedAsset finds the asset name of the published release of tag in
// repo; the caller was authorized to read repo (download links).
func (c *ReleaseApp) PublishedAsset(repo *domain.Repository, tag, name string) (*AssetView, error) {
	r, err := c.Releases.ByTag(repo.Id, tag)
	if err != nil || r.Draft {
		return nil, ErrAssetNotFound
	}
	a, err := c.Assets.ByName(r.Id, name)
	if err != nil {
		return nil, ErrAssetNotFound
	}
	return &AssetView{ReleaseAsset: a, Release: r, Repository: repo}, nil
}
