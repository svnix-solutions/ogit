package application

import (
	"fmt"
	"net/url"
	"time"
)

// GitHub's "release" event.

type HookRelease struct {
	URL             string     `json:"url"`
	HTMLURL         string     `json:"html_url"`
	Id              string     `json:"id"`
	TagName         string     `json:"tag_name"`
	TargetCommitish string     `json:"target_commitish"`
	Name            string     `json:"name"`
	Body            string     `json:"body"`
	Draft           bool       `json:"draft"`
	Prerelease      bool       `json:"prerelease"`
	CreatedAt       time.Time  `json:"created_at"`
	PublishedAt     *time.Time `json:"published_at"`
	Author          *HookUser  `json:"author"`
	TarballURL      string     `json:"tarball_url"`
	ZipballURL      string     `json:"zipball_url"`
	Assets          []any      `json:"assets"`
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
	return HookRelease{
		URL:     fmt.Sprintf("%s/api/v1/repository/%s/releases/%s", base, url.PathEscape(v.Repository.FullPath()), v.Id),
		HTMLURL: html + "/releases/tag/" + url.PathEscape(v.TagName), Id: v.Id, TagName: v.TagName, TargetCommitish: v.TargetCommitish,
		Name: v.Name, Body: v.Body, Draft: v.Draft, Prerelease: v.Prerelease, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt,
		Author: c.user(v.AuthorId), TarballURL: archive + ".tar.gz", ZipballURL: archive + ".zip", Assets: []any{},
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
