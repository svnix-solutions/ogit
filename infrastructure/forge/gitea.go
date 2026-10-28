package forge

import (
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gitea reads Gitea's (and Forgejo's) API, which follows GitHub's shapes.
type gitea struct {
	c    *client
	repo string // /repos/owner/name
}

var giteaAll = url.Values{"state": {"all"}, "limit": {"50"}}

func (g *gitea) Kind() string { return Gitea }

func (g *gitea) MaxNumbers(ctx context.Context) (int, int, error) {
	// Newest first by default.
	var issues []ghIssue
	if err := g.c.get(ctx, g.repo+"/issues", url.Values{"state": {"all"}, "limit": {"1"}, "type": {"issues"}}, &issues); err != nil {
		return 0, 0, err
	}
	var pulls []ghIssue
	if err := g.c.get(ctx, g.repo+"/issues", url.Values{"state": {"all"}, "limit": {"1"}, "type": {"pulls"}}, &pulls); err != nil {
		return 0, 0, err
	}
	n := func(l []ghIssue) int {
		if len(l) == 0 {
			return 0
		}
		return l[0].Number
	}
	return n(issues), n(pulls), nil
}

func (g *gitea) SharedNumbers() bool { return true }

func (g *gitea) Labels(ctx context.Context) ([]Label, error) {
	ls, err := all[ghLabel](ctx, g.c, g.repo+"/labels", url.Values{"limit": {"50"}})
	out := make([]Label, len(ls))
	for i, l := range ls {
		out[i] = Label{Name: l.Name, Color: color(l.Color), Description: l.Description}
	}
	return out, err
}

func (g *gitea) Milestones(ctx context.Context) ([]Milestone, error) {
	ms, err := all[ghMilestone](ctx, g.c, g.repo+"/milestones", giteaAll)
	out := make([]Milestone, len(ms))
	for i, m := range ms {
		out[i] = Milestone{Title: m.Title, Description: m.Description, Closed: m.State == "closed", DueOn: m.DueOn, ClosedAt: m.ClosedAt, CreatedAt: m.CreatedAt}
	}
	return out, err
}

func (g *gitea) Issues(ctx context.Context, fn func(Issue) error) error {
	q := url.Values{"state": {"all"}, "limit": {"50"}, "type": {"issues"}}
	return each(ctx, g.c, g.repo+"/issues", q, func(i ghIssue) error {
		if i.PullRequest != nil {
			return nil
		}
		return fn(ghToIssue(i))
	})
}

func (g *gitea) comments(ctx context.Context, number int) ([]Comment, error) {
	cs, err := all[ghComment](ctx, g.c, g.repo+"/issues/"+strconv.Itoa(number)+"/comments", nil)
	out := make([]Comment, len(cs))
	for i, c := range cs {
		out[i] = c.comment()
	}
	return out, err
}

func (g *gitea) IssueComments(ctx context.Context, number int) ([]Comment, error) {
	return g.comments(ctx, number)
}

func (g *gitea) Pulls(ctx context.Context, fn func(Pull) error) error {
	return each(ctx, g.c, g.repo+"/pulls", giteaAll, func(p ghPull) error {
		out := p.pull()
		if t, ok := strings.CutPrefix(out.Title, "WIP:"); ok && out.State == PullOpen { // Gitea's drafts
			out.Title, out.Draft = strings.TrimSpace(t), true
		}
		return fn(out)
	})
}

func (g *gitea) PullComments(ctx context.Context, p Pull) ([]Comment, error) {
	return g.comments(ctx, p.Number)
}

type giteaReviewComment struct {
	User             *ghUser   `json:"user"`
	Body             string    `json:"body"`
	Path             string    `json:"path"`
	Position         int       `json:"position"`
	OriginalPosition int       `json:"original_position"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func (g *gitea) Reviews(ctx context.Context, p Pull) ([]Review, error) {
	n := strconv.Itoa(p.Number)
	rs, err := all[ghReview](ctx, g.c, g.repo+"/pulls/"+n+"/reviews", url.Values{"limit": {"50"}})
	if err != nil {
		return nil, err
	}
	var out []Review
	for _, r := range rs {
		switch strings.ToUpper(r.State) {
		case "PENDING", "REQUEST_REVIEW":
			continue
		}
		review := Review{Author: r.User.user(), State: verdict(r.State), Body: r.Body, CommitId: r.CommitId, SubmittedAt: r.SubmittedAt}
		cs, err := all[giteaReviewComment](ctx, g.c, g.repo+"/pulls/"+n+"/reviews/"+strconv.FormatInt(r.Id, 10)+"/comments", nil)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			line := c.Position
			if line == 0 {
				line = c.OriginalPosition
			}
			review.Comments = append(review.Comments, Comment{Author: c.User.user(), Body: c.Body, Path: c.Path, Line: line, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt})
		}
		out = append(out, review)
	}
	return out, nil
}

func (g *gitea) Releases(ctx context.Context) ([]Release, error) {
	rs, err := all[ghRelease](ctx, g.c, g.repo+"/releases", url.Values{"limit": {"50"}})
	out := make([]Release, len(rs))
	for i, r := range rs {
		out[i] = ghToRelease(r)
		for j, a := range r.Assets {
			out[i].Assets[j].URL, out[i].Assets[j].ContentType = a.BrowserDownloadURL, ""
		}
	}
	return out, err
}

func (g *gitea) Download(ctx context.Context, a Asset) (io.ReadCloser, error) {
	return g.c.download(ctx, a.URL, "")
}

func (g *gitea) PullRefs(ref string) bool { return pullHeadRef(ref, "refs/pull/") }
