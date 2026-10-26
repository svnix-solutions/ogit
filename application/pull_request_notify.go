package application

import (
	"fmt"
	"strings"

	"github.com/labbs/ogit/domain"
)

// notify emails a pull request's participants about actorId's activity:
// the author, reviewers, commenters and anyone @mentioned in text, if they
// can read the repository; never the actor. It returns at once: mail goes
// out in the background so a slow SMTP server doesn't slow the request.
func (c *PullRequestApp) notify(view *PullRequestView, actorId, verb, text string) {
	if c.Mailer == nil || c.Config.SMTP.Host == "" {
		return
	}
	pr, repo := *view.PullRequest, *view.Repository
	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.Logger.Error().Interface("panic", r).Str("component", "application.pull_request.notify").Msg("notification failed")
			}
		}()
		c.sendNotifications(&repo, &pr, actorId, verb, text)
	}()
}

func (c *PullRequestApp) sendNotifications(repo *domain.Repository, pr *domain.PullRequest, actorId, verb, text string) {
	logger := c.Logger.With().Str("component", "application.pull_request.notify").Logger()
	ids := map[string]bool{}
	if pr.AuthorId != nil {
		ids[*pr.AuthorId] = true
	}
	if c.Assignees != nil {
		if assigned, err := c.Assignees.For(domain.AssignPullRequest, []string{pr.Id}); err == nil {
			for _, u := range assigned[pr.Id] {
				ids[u.Id] = true
			}
		}
	}
	if c.Reviewers != nil {
		if requested, err := c.Reviewers.For([]string{pr.Id}); err == nil {
			for _, u := range requested[pr.Id] {
				ids[u.Id] = true
			}
		}
	}
	if reviews, err := c.Reviews.Reviews(pr.Id); err == nil {
		for _, r := range reviews {
			if r.AuthorId != nil {
				ids[*r.AuthorId] = true
			}
		}
	}
	if comments, err := c.Reviews.Comments(pr.Id); err == nil {
		for _, cm := range comments {
			if cm.AuthorId != nil {
				ids[*cm.AuthorId] = true
			}
		}
	}
	for _, name := range Mentions(text) {
		if u, err := c.Users.GetByUsername(name); err == nil {
			ids[u.Id] = true
		}
	}
	delete(ids, actorId)

	actor := "someone"
	if u, err := c.Users.GetById(actorId); err == nil {
		actor = u.Username
	}
	subject := fmt.Sprintf("[%s] %s (#%d)", repo.FullPath(), pr.Title, pr.Number)
	body := fmt.Sprintf("%s %s pull request #%d in %s:\n", actor, verb, pr.Number, repo.FullPath())
	if text = strings.TrimSpace(text); text != "" {
		body += "\n" + text + "\n"
	}
	if url := strings.TrimSuffix(c.Config.Server.PublicURL, "/"); url != "" {
		body += fmt.Sprintf("\n%s/%s/pulls/%d\n", url, repo.FullPath(), pr.Number)
	}

	for id := range ids {
		user, err := c.Users.GetById(id)
		if err != nil || !user.Active || user.Email == "" {
			continue
		}
		if ok, err := c.Repositories.CanUserAccess(repo.Id, id, "read"); err != nil || !ok {
			continue // e.g. @mentioned but not a member of a private repository
		}
		if err := c.Mailer.Send(user.Email, subject, body); err != nil {
			logger.Error().Err(err).Str("user_id", id).Msg("failed to send pull request notification")
		}
	}
}
