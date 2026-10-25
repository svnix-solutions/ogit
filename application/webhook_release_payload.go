package application

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
)

// GitHub's "release" event.

type HookRelease struct {
	URL             string      `json:"url"`
	HTMLURL         string      `json:"html_url"`
	Id              string      `json:"id"`
	TagName         string      `json:"tag_name"`
	TargetCommitish string      `json:"target_commitish"`
	Name            string      `json:"name"`
	Body            string      `json:"body"`
	Draft           bool        `json:"draft"`
	Prerelease      bool        `json:"prerelease"`
	CreatedAt       time.Time   `json:"created_at"`
	PublishedAt     *time.Time  `json:"published_at"`
	Author          *HookUser   `json:"author"`
	TarballURL      string      `json:"tarball_url"`
	ZipballURL      string      `json:"zipball_url"`
	Assets          []HookAsset `json:"assets"`
}

type HookAsset struct {
	URL                string    `json:"url"`
	BrowserDownloadURL string    `json:"browser_download_url"`
	Id                 string    `json:"id"`
	Name               string    `json:"name"`
	Label              string    `json:"label"`
	State              string    `json:"state"`
	ContentType        string    `json:"content_type"`
	Size               int64     `json:"size"`
	DownloadCount      int64     `json:"download_count"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Uploader           *HookUser `json:"uploader"`
}

// AssetDownloadURL is the public download link of an asset of a published
// release: <base>/<owner>/<repo>/releases/download/<tag>/<name>.
func AssetDownloadURL(base string, repo *domain.Repository, tag, name string) string {
	return base + "/" + repo.FullPath() + "/releases/download/" + escapePath(tag) + "/" + url.PathEscape(name)
}

// escapePath escapes each segment of a slash-separated name.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

type ReleasePayload struct {
	Action     string         `json:"action"`
	Release    HookRelease    `json:"release"`
	Changes    map[string]any `json:"changes,omitempty"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

func (c *WebhookApp) hookRelease(v *ReleaseView) HookRelease {
	base := c.baseURL()
	html := base + "/" + v.Repository.FullPath()
	archive := html + "/archive/" + url.PathEscape(v.TagName)
	assets := make([]HookAsset, 0, len(v.Assets))
	for _, a := range v.Assets {
		h := HookAsset{
			URL:                fmt.Sprintf("%s/api/v1/repository/%s/releases/%s/assets/%s", base, url.PathEscape(v.Repository.FullPath()), v.Id, a.Id),
			BrowserDownloadURL: AssetDownloadURL(base, v.Repository, v.TagName, a.Name), Id: a.Id, Name: a.Name, Label: a.Label,
			State: "uploaded", ContentType: a.ContentType, Size: a.Size, DownloadCount: a.DownloadCount, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt,
		}
		if a.UploaderId != nil {
			h.Uploader = c.user(a.UploaderId)
		}
		assets = append(assets, h)
	}
	return HookRelease{
		URL:     fmt.Sprintf("%s/api/v1/repository/%s/releases/%s", base, url.PathEscape(v.Repository.FullPath()), v.Id),
		HTMLURL: html + "/releases/tag/" + url.PathEscape(v.TagName), Id: v.Id, TagName: v.TagName, TargetCommitish: v.TargetCommitish,
		Name: v.Name, Body: v.Body, Draft: v.Draft, Prerelease: v.Prerelease, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt,
		Author: c.user(v.AuthorId), TarballURL: archive + ".tar.gz", ZipballURL: archive + ".zip", Assets: assets,
	}
}

// ReleaseEvent queues a release event (published releases only).
func (c *WebhookApp) ReleaseEvent(v *ReleaseView, action, actorId string, changes map[string]any) {
	if c == nil {
		return
	}
	sender := c.sender(actorId, "")
	if actorId == "" {
		sender = HookUser{Login: "repository token", Type: "Bot"}
	}
	c.emit(v.Repository, "release", action, ReleasePayload{Action: action, Release: c.hookRelease(v), Changes: changes,
		Repository: c.repositoryPayload(v.Repository), Sender: sender})
}
