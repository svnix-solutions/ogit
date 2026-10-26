// Package notifications serves the inbox (/api/v1/notifications), email
// preferences, and repository watch levels
// (/api/v1/repository/:identifier/watch). Rules: application/notification_app.go.
package notifications

import (
	"time"

	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

type Controller struct {
	Logger        zerolog.Logger
	Notifications *application.NotificationApp
}

type Subject struct {
	Type   string `json:"type"` // issue, pull_request, release
	Number int    `json:"number,omitempty"`
	Title  string `json:"title"`
	URL    string `json:"url"` // web path
}

type Notification struct {
	Id            string    `json:"id"`
	Unread        bool      `json:"unread"`
	Reason        string    `json:"reason"` // mention, review_requested, assign, author, comment, ci_activity, subscribed
	Participating bool      `json:"participating"`
	Repository    string    `json:"repository"` // full path
	Subject       Subject   `json:"subject"`
	Summary       string    `json:"summary"`
	Actor         string    `json:"actor,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ListRequest struct {
	All           bool   `query:"all" json:"-"`           // read ones too
	Participating bool   `query:"participating" json:"-"` // only threads you take part in
	Repository    string `query:"repository" json:"-"`    // id or path (owner%2Frepo)
	Offset        int    `query:"offset" json:"-"`
	Limit         int    `query:"limit" json:"-"`
}

type ListResponse struct {
	Notifications []Notification `json:"notifications"`
	Total         int64          `json:"total"`
	Unread        int64          `json:"unread"`
}

type CountResponse struct {
	Unread int64 `json:"unread"`
}

type IdRequest struct {
	Id string `path:"id" json:"-" validate:"required"`
}

type UpdateRequest struct {
	Id     string `path:"id" json:"-" validate:"required"`
	Unread bool   `json:"unread"`
}

type ReadAllRequest struct {
	Repository string `json:"repository"` // only this repository (id or path)
}

type MessageResponse struct {
	Message string `json:"message"`
}

type MuteResponse struct {
	Muted bool `json:"muted"`
}

type Settings struct {
	Email string `json:"email" validate:"required"` // participating, all, none
}

type WatchRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
}

type SetWatchRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Level      string `json:"level" validate:"required"` // participating, all, releases, ignore
}

type WatchResponse struct {
	Level string `json:"level"`
}

func Setup(c Controller, grp *fiberoapi.OApiGroup, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Notifications"}
	g := grp.Group("/notifications")
	fiberoapi.Get(g, "", c.List, fiberoapi.OpenAPIOptions{Summary: "Your notifications", OperationID: "notifications.list", Tags: tags,
		Description: "One per thread (issue, pull request, release), newest first. Unread only unless ?all=true; ?participating=true leaves out watched activity; ?repository= one repository."})
	fiberoapi.Get(g, "/count", c.Count, fiberoapi.OpenAPIOptions{Summary: "Unread notifications", OperationID: "notifications.count", Tags: tags})
	fiberoapi.Post(g, "/read", c.ReadAll, fiberoapi.OpenAPIOptions{Summary: "Mark all as read", OperationID: "notifications.read_all", Tags: tags,
		Description: "Everything up to now (repository: only one repository)."})
	fiberoapi.Get(g, "/settings", c.GetSettings, fiberoapi.OpenAPIOptions{Summary: "Notification email settings", OperationID: "notifications.settings.get", Tags: tags})
	fiberoapi.Put(g, "/settings", c.SetSettings, fiberoapi.OpenAPIOptions{Summary: "Change notification email settings", OperationID: "notifications.settings.set", Tags: tags,
		Description: "email: participating (threads you take part in; the default), all (also watched activity), none."})
	fiberoapi.Patch(g, "/:id", c.Update, fiberoapi.OpenAPIOptions{Summary: "Mark a notification read or unread", OperationID: "notifications.update", Tags: tags})
	fiberoapi.Post(g, "/:id/mute", c.Mute, fiberoapi.OpenAPIOptions{Summary: "Unsubscribe from a thread", OperationID: "notifications.mute", Tags: tags,
		Description: "No more notifications for this issue, pull request or release (marks it read)."})
	fiberoapi.Delete(g, "/:id/mute", c.Unmute, fiberoapi.OpenAPIOptions{Summary: "Subscribe to a thread again", OperationID: "notifications.unmute", Tags: tags})

	fiberoapi.Get(repoGrp, "/:identifier/watch", c.GetWatch, fiberoapi.OpenAPIOptions{Summary: "Your watch level", OperationID: "repository.watch.get", Tags: tags})
	fiberoapi.Put(repoGrp, "/:identifier/watch", c.SetWatch, fiberoapi.OpenAPIOptions{Summary: "Watch a repository", OperationID: "repository.watch.set", Tags: tags,
		Description: "participating (only threads you take part in; the default), all (also new issues, pull requests and releases), releases (also releases), ignore (nothing, not even mentions)."})
}

func toNotification(n domain.Notification) Notification {
	out := Notification{Id: n.Id, Unread: n.Unread, Reason: n.Reason, Participating: n.Participating, Summary: n.Summary, UpdatedAt: n.UpdatedAt,
		Subject: Subject{Type: n.ThreadType, Number: n.ThreadNumber, Title: n.Title, URL: n.URL}}
	if n.Repository != nil {
		out.Repository = n.Repository.FullPath()
	}
	if n.Actor != nil {
		out.Actor = n.Actor.Username
	}
	return out
}

func user(ctx *fiber.Ctx) (string, *fiberoapi.ErrorResponse) {
	auth, err := fiberoapi.GetAuthContext(ctx)
	if err != nil {
		return "", apierror.Unauthenticated()
	}
	return auth.UserID, nil
}

// repositoryId resolves a repository the caller can read ("" for none).
func (c Controller) repositoryId(identifier, userId string) (string, error) {
	if identifier == "" {
		return "", nil
	}
	id, path := repoid.Split(identifier)
	repo, _, err := c.Notifications.Watch(id, path, userId)
	if err != nil {
		return "", err
	}
	return repo.Id, nil
}

func (c Controller) List(ctx *fiber.Ctx, req ListRequest) (*ListResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	repoId, err := c.repositoryId(req.Repository, userId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	list, total, unread, err := c.Notifications.List(userId, domain.NotificationFilter{All: req.All, Participating: req.Participating, RepositoryId: repoId}, req.Offset, req.Limit)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	resp := &ListResponse{Notifications: make([]Notification, 0, len(list)), Total: total, Unread: unread}
	for _, n := range list {
		resp.Notifications = append(resp.Notifications, toNotification(n))
	}
	return resp, nil
}

func (c Controller) Count(ctx *fiber.Ctx, _ struct{}) (*CountResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	n, err := c.Notifications.UnreadCount(userId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &CountResponse{Unread: n}, nil
}

func (c Controller) Update(ctx *fiber.Ctx, req UpdateRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	if err := c.Notifications.SetUnread(userId, req.Id, req.Unread); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "updated"}, nil
}

func (c Controller) ReadAll(ctx *fiber.Ctx, req ReadAllRequest) (*MessageResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	repoId, err := c.repositoryId(req.Repository, userId)
	if err == nil {
		err = c.Notifications.MarkAllRead(userId, repoId)
	}
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MessageResponse{Message: "marked as read"}, nil
}

func (c Controller) mute(ctx *fiber.Ctx, id string, on bool) (*MuteResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	if _, err := c.Notifications.Unsubscribe(userId, id, on); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &MuteResponse{Muted: on}, nil
}

func (c Controller) Mute(ctx *fiber.Ctx, req IdRequest) (*MuteResponse, *fiberoapi.ErrorResponse) {
	return c.mute(ctx, req.Id, true)
}

func (c Controller) Unmute(ctx *fiber.Ctx, req IdRequest) (*MuteResponse, *fiberoapi.ErrorResponse) {
	return c.mute(ctx, req.Id, false)
}

func (c Controller) GetSettings(ctx *fiber.Ctx, _ struct{}) (*Settings, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	level, err := c.Notifications.EmailLevel(userId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &Settings{Email: string(level)}, nil
}

func (c Controller) SetSettings(ctx *fiber.Ctx, req Settings) (*Settings, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	if err := c.Notifications.SetEmailLevel(userId, domain.EmailLevel(req.Email)); err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &req, nil
}

func (c Controller) GetWatch(ctx *fiber.Ctx, req WatchRequest) (*WatchResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	_, level, err := c.Notifications.Watch(id, path, userId)
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &WatchResponse{Level: string(level)}, nil
}

func (c Controller) SetWatch(ctx *fiber.Ctx, req SetWatchRequest) (*WatchResponse, *fiberoapi.ErrorResponse) {
	userId, e := user(ctx)
	if e != nil {
		return nil, e
	}
	id, path := repoid.Split(req.Identifier)
	level, err := c.Notifications.SetWatch(id, path, userId, domain.WatchLevel(req.Level))
	if err != nil {
		return nil, apierror.From(err, c.Logger)
	}
	return &WatchResponse{Level: string(level)}, nil
}
