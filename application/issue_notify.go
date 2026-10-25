package application

import (
	"fmt"
	"strings"

	"github.com/labbs/ogit/domain"
)

// notify emails an issue's participants about actorId's activity: the
// author, assignees, commenters, anyone @mentioned in text, and extra (new
// assignees), if they can read the repository; never the actor. Mail goes
// out in the background.
func (c *IssueApp) notify(v *IssueView, actorId, verb, text string, extra []domain.User) {
	if c.Mailer == nil || c.Config.SMTP.Host == "" {
		return
	}
	issue, repo, assignees := *v.Issue, *v.Repository, v.Assignees
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.Logger.Error().Interface("panic", r).Str("component", "application.issue.notify").Msg("notification failed")
			}
		}()
		ids := map[string]bool{}
		if issue.AuthorId != nil {
			ids[*issue.AuthorId] = true
		}
		for _, u := range append(assignees, extra...) {
			ids[u.Id] = true
		}
		if comments, err := c.Issues.Comments(issue.Id); err == nil {
			for _, cm := range comments {
				if cm.AuthorId != nil {
					ids[*cm.AuthorId] = true
				}
			}
		}
		for _, name := range Mentions(issue.Body + "\n" + text) {
			if u, err := c.Repositories.UserPers.GetByUsername(name); err == nil {
				ids[u.Id] = true
			}
		}
		delete(ids, actorId)
		c.sendIssueMail(&repo, &issue, ids, actorId, verb, text)
	}()
}

func (c *IssueApp) sendIssueMail(repo *domain.Repository, issue *domain.Issue, ids map[string]bool, actorId, verb, text string) {
	logger := c.Logger.With().Str("component", "application.issue.notify").Logger()
	actor := "someone"
	if u, err := c.Repositories.UserPers.GetById(actorId); err == nil {
		actor = u.Username
	}
	subject := fmt.Sprintf("[%s] %s (#%d)", repo.FullPath(), issue.Title, issue.Number)
	body := fmt.Sprintf("%s %s issue #%d in %s:\n", actor, verb, issue.Number, repo.FullPath())
	if text = strings.TrimSpace(text); text != "" {
		body += "\n" + text + "\n"
	}
	if url := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); url != "" {
		body += fmt.Sprintf("\n%s/%s/issues/%d\n", url, repo.FullPath(), issue.Number)
	}
	for id := range ids {
		user, err := c.Repositories.UserPers.GetById(id)
		if err != nil || !user.Active || user.Email == "" {
			continue
		}
		if ok, err := c.Repositories.CanUserAccess(repo.Id, id, "read"); err != nil || !ok {
			continue // e.g. @mentioned but not a member of a private repository
		}
		if err := c.Mailer.Send(user.Email, subject, body); err != nil {
			logger.Error().Err(err).Str("user_id", id).Msg("failed to send issue notification")
		}
	}
}
