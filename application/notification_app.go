package application

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/mail"
	"github.com/rs/zerolog"
)

// Notification rules:
//   - a notification is a thread (issue, pull request, release) in a user's
//     inbox; each new event on it makes it unread again, with the latest
//     reason and summary;
//   - participants are always notified: authors, assignees, requested
//     reviewers, reviewers and commenters, @mentioned users, and pull
//     request authors when a check fails on their head;
//   - repository watch levels: participating (default), all (also new
//     issues, pull requests and releases), releases (also releases), ignore
//     (nothing, not even mentions). Repository creators start on all;
//   - never the actor; never someone who can't read the repository;
//     never someone who unsubscribed from the thread;
//   - email follows each user's preference: participating (default: emails
//     for threads they take part in), all (also watched activity), none.
//     Mail goes out in the background; the inbox row is written at once.

var (
	ErrNotificationNotFound = errors.New("notification not found")
	ErrInvalidWatch         = errors.New("watch level must be participating, all, releases or ignore")
	ErrInvalidEmailLevel    = errors.New("email must be participating, all or none")
)

// reasonRank: when someone has several reasons, the highest is shown.
var reasonRank = map[string]int{
	domain.ReasonReviewRequested: 7, domain.ReasonMention: 6, domain.ReasonAssign: 5, domain.ReasonCIActivity: 4,
	domain.ReasonAuthor: 3, domain.ReasonComment: 2, domain.ReasonSubscribed: 1,
}

// Recipients collects user ids with their reason.
type Recipients map[string]string

// Add records a reason for userId, keeping the strongest.
func (r Recipients) Add(userId, reason string) {
	if userId == "" {
		return
	}
	if reasonRank[reason] > reasonRank[r[userId]] {
		r[userId] = reason
	}
}

// Event is something that happened on a thread.
type Event struct {
	Repo       *domain.Repository
	ThreadType string
	ThreadId   string
	Number     int
	Title      string
	Path       string // the thread's web path
	ActorId    string
	// Summary is the inbox line ("dev commented on issue #3"); Text is the
	// comment or description, for emails.
	Summary string
	Text    string
	// Participants are notified whatever their watch level (except ignore).
	Participants Recipients
	// WatchLevels: watchers at these levels are notified too (new issues,
	// pull requests, releases).
	WatchLevels []domain.WatchLevel
}

type NotificationApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.NotificationPers
	Repositories *RepositoryApp
	Users        domain.UserPers
	Mailer       mail.Sender
}

// Notify records an event for everyone concerned, then emails them in the
// background. It never fails the action that caused it.
func (c *NotificationApp) Notify(ev Event) {
	if c == nil {
		return
	}
	logger := c.Logger.With().Str("component", "application.notification").Logger()
	to := Recipients{}
	for id, reason := range ev.Participants {
		to.Add(id, reason)
	}
	if len(ev.WatchLevels) > 0 {
		watchers, err := c.Pers.Watchers(ev.Repo.Id, ev.WatchLevels...)
		if err != nil {
			logger.Error().Err(err).Msg("failed to list watchers")
		}
		for _, id := range watchers {
			to.Add(id, domain.ReasonSubscribed)
		}
	}
	delete(to, ev.ActorId)
	ids := make([]string, 0, len(to))
	for id := range to {
		if domain.IsPerson(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	ignoring, err := c.Pers.Ignoring(ev.Repo.Id, ids)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read watch levels")
		return
	}
	muted, err := c.Pers.Muted(ev.ThreadType, ev.ThreadId, ids)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read unsubscriptions")
		return
	}
	var actor *string
	if domain.IsPerson(ev.ActorId) {
		actor = &ev.ActorId
	}
	type mailTo struct {
		user          domain.User
		participating bool
	}
	var mails []mailTo
	for _, id := range ids {
		if ignoring[id] || muted[id] {
			continue
		}
		user, err := c.Users.GetById(id)
		if err != nil || !user.Active {
			continue
		}
		// An @mention must not leak a private repository.
		if ok, err := c.Repositories.CanUserAccess(ev.Repo.Id, id, "read"); err != nil || !ok {
			continue
		}
		reason := to[id]
		n := &domain.Notification{UserId: id, RepositoryId: ev.Repo.Id, ThreadType: ev.ThreadType, ThreadId: ev.ThreadId, ThreadNumber: ev.Number,
			Title: ev.Title, URL: ev.Path, Reason: reason, Participating: reason != domain.ReasonSubscribed, Summary: ev.Summary, ActorId: actor}
		if err := c.Pers.Upsert(n); err != nil {
			logger.Error().Err(err).Str("user_id", id).Msg("failed to record a notification")
			continue
		}
		mails = append(mails, mailTo{user: user, participating: n.Participating})
	}
	if c.Mailer == nil || c.Config.SMTP.Host == "" || len(mails) == 0 {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error().Interface("panic", r).Msg("notification email failed")
			}
		}()
		subject, body := c.email(ev)
		for _, m := range mails {
			level, err := c.Pers.EmailLevel(m.user.Id)
			if err != nil || level == domain.EmailNone || (!m.participating && level != domain.EmailAll) || m.user.Email == "" {
				continue
			}
			if err := c.Mailer.Send(m.user.Email, subject, body); err != nil {
				logger.Error().Err(err).Str("user_id", m.user.Id).Msg("failed to send a notification email")
			}
		}
	}()
}

func (c *NotificationApp) email(ev Event) (string, string) {
	subject := fmt.Sprintf("[%s] %s", ev.Repo.FullPath(), ev.Title)
	if ev.Number > 0 {
		subject += fmt.Sprintf(" (#%d)", ev.Number)
	}
	body := ev.Summary + " in " + ev.Repo.FullPath() + ":\n"
	if text := strings.TrimSpace(ev.Text); text != "" {
		body += "\n" + text + "\n"
	}
	if url := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); url != "" {
		body += "\n" + url + ev.Path + "\n"
	}
	body += "\nYou can change which emails you get in your notification settings.\n"
	return subject, body
}

// ActorName is how an event's actor is named in summaries.
func (c *NotificationApp) ActorName(actorId string) string {
	if c != nil && domain.IsPerson(actorId) {
		if u, err := c.Users.GetById(actorId); err == nil {
			return u.Username
		}
	}
	return "someone"
}

// ThreadDeleted removes a deleted issue's or release's notifications.
func (c *NotificationApp) ThreadDeleted(threadType, threadId string) {
	if c == nil {
		return
	}
	if err := c.Pers.DeleteThread(threadType, threadId); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.notification").Msg("failed to delete a thread's notifications")
	}
}

// RepositoryCreated: creators watch their new repositories (all activity).
func (c *NotificationApp) RepositoryCreated(repo *domain.Repository, creatorId string) {
	if c == nil || !domain.IsPerson(creatorId) {
		return
	}
	if err := c.Pers.SetWatch(creatorId, repo.Id, domain.WatchAll); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.notification").Msg("failed to watch a new repository")
	}
}

// List returns the user's inbox page, its total and the unread count.
func (c *NotificationApp) List(userId string, f domain.NotificationFilter, offset, limit int) ([]domain.Notification, int64, int64, error) {
	list, total, err := c.Pers.List(userId, f, max(offset, 0), clampLimit(limit))
	if err != nil {
		return nil, 0, 0, err
	}
	unread, err := c.Pers.UnreadCount(userId)
	return list, total, unread, err
}

func (c *NotificationApp) UnreadCount(userId string) (int64, error) {
	return c.Pers.UnreadCount(userId)
}

func (c *NotificationApp) SetUnread(userId, id string, unread bool) error {
	if err := c.Pers.SetUnread(userId, id, unread); isNotFound(err) {
		return ErrNotificationNotFound
	} else {
		return err
	}
}

// MarkAllRead marks everything read up to now (one repository with
// repositoryId), so a notification arriving meanwhile stays unread.
func (c *NotificationApp) MarkAllRead(userId, repositoryId string) error {
	return c.Pers.MarkAllRead(userId, repositoryId, time.Now())
}

// Unsubscribe mutes a notification's thread for the user (and marks it
// read); Resubscribe undoes it.
func (c *NotificationApp) Unsubscribe(userId, id string, mute bool) (*domain.Notification, error) {
	n, err := c.Pers.Get(userId, id)
	if isNotFound(err) {
		return nil, ErrNotificationNotFound
	} else if err != nil {
		return nil, err
	}
	if mute {
		if err := c.Pers.Mute(userId, n.ThreadType, n.ThreadId); err != nil {
			return nil, err
		}
		if err := c.Pers.SetUnread(userId, id, false); err != nil {
			return nil, err
		}
		n.Unread = false
	} else if err := c.Pers.Unmute(userId, n.ThreadType, n.ThreadId); err != nil {
		return nil, err
	}
	return n, nil
}

func (c *NotificationApp) Muted(userId string, n *domain.Notification) (bool, error) {
	return c.Pers.IsMuted(userId, n.ThreadType, n.ThreadId)
}

// Watch returns the caller's watch level on a repository they can read.
func (c *NotificationApp) Watch(id, path *string, userId string) (*domain.Repository, domain.WatchLevel, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return nil, "", err
	}
	level, err := c.Pers.Watch(userId, repo.Id)
	return repo, level, err
}

func (c *NotificationApp) SetWatch(id, path *string, userId string, level domain.WatchLevel) (domain.WatchLevel, error) {
	if !level.Valid() {
		return "", ErrInvalidWatch
	}
	repo, err := c.Repositories.GetRepositoryForUser(id, path, userId)
	if err != nil {
		return "", err
	}
	return level, c.Pers.SetWatch(userId, repo.Id, level)
}

func (c *NotificationApp) EmailLevel(userId string) (domain.EmailLevel, error) {
	return c.Pers.EmailLevel(userId)
}

func (c *NotificationApp) SetEmailLevel(userId string, level domain.EmailLevel) error {
	switch level {
	case domain.EmailParticipating, domain.EmailAll, domain.EmailNone:
		return c.Pers.SetEmailLevel(userId, level)
	}
	return ErrInvalidEmailLevel
}
