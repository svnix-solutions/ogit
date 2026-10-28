package application

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
)

// notify tells a pull request's participants about actorId's activity
// (notification_app.go): the author, assignees, requested reviewers,
// reviewers, commenters and anyone @mentioned in text. "opened" also
// reaches the repository's watchers.
func (c *PullRequestApp) notify(view *PullRequestView, actorId, verb, text string) {
	if c.Notifications == nil {
		return
	}
	pr, repo := view.PullRequest, view.Repository
	to := Recipients{}
	if pr.AuthorId != nil {
		to.Add(*pr.AuthorId, domain.ReasonAuthor)
	}
	if reviews, err := c.Reviews.Reviews(pr.Id); err == nil {
		for _, r := range reviews {
			if r.AuthorId != nil {
				to.Add(*r.AuthorId, domain.ReasonComment)
			}
		}
	}
	if comments, err := c.Reviews.Comments(pr.Id); err == nil {
		for _, cm := range comments {
			if cm.AuthorId != nil {
				to.Add(*cm.AuthorId, domain.ReasonComment)
			}
		}
	}
	if c.Assignees != nil {
		if assigned, err := c.Assignees.For(domain.AssignPullRequest, []string{pr.Id}); err == nil {
			for _, u := range assigned[pr.Id] {
				to.Add(u.Id, domain.ReasonAssign)
			}
		}
	}
	if c.Reviewers != nil {
		if requested, err := c.Reviewers.For([]string{pr.Id}); err == nil {
			for _, u := range requested[pr.Id] {
				to.Add(u.Id, domain.ReasonReviewRequested)
			}
		}
	}
	for _, name := range Mentions(text) {
		if u, err := c.Users.GetByUsername(name); err == nil {
			to.Add(u.Id, domain.ReasonMention)
		}
	}
	ev := Event{Repo: repo, ThreadType: domain.ThreadPullRequest, ThreadId: pr.Id, Number: pr.Number, Title: pr.Title,
		Path: fmt.Sprintf("/%s/pulls/%d", repo.FullPath(), pr.Number), ActorId: actorId,
		Summary: fmt.Sprintf("%s %s pull request #%d", c.Notifications.ActorName(actorId), verb, pr.Number), Text: text, Participants: to}
	if verb == "opened" {
		ev.WatchLevels = []domain.WatchLevel{domain.WatchAll}
	}
	c.Notifications.Notify(ev)
}

// StatusFailed notifies the authors of open pull requests whose head is sha
// that a check failed (StatusApp.Failed).
func (c *PullRequestApp) StatusFailed(repo *domain.Repository, st storer.Storer, sha string, s *domain.CommitStatus) {
	if c.Notifications == nil {
		return
	}
	prs, _, err := c.Pulls.List(repo.Id, domain.PullRequestOpen, "", 0, 200)
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.pull_request.notify").Msg("failed to list pull requests")
		return
	}
	for i := range prs {
		pr := &prs[i]
		if pr.AuthorId == nil {
			continue
		}
		if tip, _, ok := c.headTip(st, repo, pr, false); !ok || tip.Hash.String() != sha {
			continue
		}
		summary := fmt.Sprintf("%s: %s on pull request #%d", s.Context, s.State, pr.Number)
		if s.Description != "" {
			summary += " (" + s.Description + ")"
		}
		c.Notifications.Notify(Event{Repo: repo, ThreadType: domain.ThreadPullRequest, ThreadId: pr.Id, Number: pr.Number, Title: pr.Title,
			Path: fmt.Sprintf("/%s/pulls/%d", repo.FullPath(), pr.Number), Summary: summary, Text: s.TargetURL,
			Participants: Recipients{*pr.AuthorId: domain.ReasonCIActivity}})
	}
}
