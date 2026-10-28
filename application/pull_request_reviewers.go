package application

import (
	"errors"
	"strings"

	"github.com/labbs/ogit/domain"
)

// Requested reviewer rules:
//   - the pull request's author and writers (developers and up) request
//     reviews, on open pull requests of repositories that are not archived;
//   - reviewers are active users who can read the repository, never the
//     author; at most 10 at a time;
//   - a request goes away when that reviewer submits a review;
//   - "review_requested" / "review_request_removed" pull_request webhook
//     events, like GitHub; requested reviewers are participants (notified).

var ErrInvalidReviewer = errors.New("reviewers must be users who can read the repository, not the author; at most 10")

const maxReviewers = 10

// SetReviewers replaces the reviewers requested on a pull request, by
// username. It returns them.
func (c *PullRequestApp) SetReviewers(id, path *string, actorId string, number int, names []string) ([]domain.User, error) {
	view, err := c.Get(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	isAuthor := view.AuthorId != nil && *view.AuthorId == actorId
	if !view.Role.CanWrite() && !isAuthor {
		return nil, ErrForbidden
	}
	if view.Repository.IsArchived {
		return nil, ErrGitArchived
	}
	if view.State != domain.PullRequestOpen {
		return nil, ErrNotOpen
	}
	if len(names) > maxReviewers {
		return nil, ErrInvalidReviewer
	}
	var users []domain.User
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if seen[name] {
			continue
		}
		seen[name] = true
		u, err := c.Users.GetByUsername(name)
		if err != nil || !u.Active || (view.AuthorId != nil && *view.AuthorId == u.Id) {
			return nil, ErrInvalidReviewer
		}
		if ok, err := c.Repositories.CanUserAccess(view.RepositoryId, u.Id, "read"); err != nil {
			return nil, err
		} else if !ok {
			return nil, ErrInvalidReviewer
		}
		users = append(users, u)
	}
	if err := c.Reviewers.Set(view.Id, userIds(users), actorId); err != nil {
		return nil, err
	}
	added, removed := assignmentDiff(view.RequestedReviewers, users)
	for _, u := range added {
		c.Webhooks.PullRequestEvent(view, "review_requested", actorId, PullRequestPayload{RequestedReviewer: c.Webhooks.hookUserOrNil(u)})
	}
	for _, u := range removed {
		c.Webhooks.PullRequestEvent(view, "review_request_removed", actorId, PullRequestPayload{RequestedReviewer: c.Webhooks.hookUserOrNil(u)})
	}
	if len(added) > 0 {
		view.RequestedReviewers = users
		c.notify(view, actorId, "requested a review from "+usernames(added)+" on", "")
	}
	if users == nil {
		users = []domain.User{}
	}
	return users, nil
}
