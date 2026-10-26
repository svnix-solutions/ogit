package application

import (
	"fmt"

	"github.com/labbs/ogit/domain"
)

// notify tells an issue's participants about actorId's activity
// (notification_app.go): the author, assignees, commenters, anyone
// @mentioned in the issue or in text, and extra (new assignees). "opened"
// also reaches the repository's watchers.
func (c *IssueApp) notify(v *IssueView, actorId, verb, text string, extra []domain.User) {
	if c.Notifications == nil {
		return
	}
	issue, repo := v.Issue, v.Repository
	to := Recipients{}
	if issue.AuthorId != nil {
		to.Add(*issue.AuthorId, domain.ReasonAuthor)
	}
	if comments, err := c.Issues.Comments(issue.Id); err == nil {
		for _, cm := range comments {
			if cm.AuthorId != nil {
				to.Add(*cm.AuthorId, domain.ReasonComment)
			}
		}
	}
	for _, u := range append(v.Assignees, extra...) {
		to.Add(u.Id, domain.ReasonAssign)
	}
	for _, name := range Mentions(issue.Body + "\n" + text) {
		if u, err := c.Repositories.UserPers.GetByUsername(name); err == nil {
			to.Add(u.Id, domain.ReasonMention)
		}
	}
	ev := Event{Repo: repo, ThreadType: domain.ThreadIssue, ThreadId: issue.Id, Number: issue.Number, Title: issue.Title,
		Path: fmt.Sprintf("/%s/issues/%d", repo.FullPath(), issue.Number), ActorId: actorId,
		Summary: fmt.Sprintf("%s %s issue #%d", c.Notifications.ActorName(actorId), verb, issue.Number), Text: text, Participants: to}
	if verb == "opened" {
		ev.WatchLevels = []domain.WatchLevel{domain.WatchAll}
	}
	c.Notifications.Notify(ev)
}
