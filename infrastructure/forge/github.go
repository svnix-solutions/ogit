package forge

import (
	"context"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// github reads GitHub's REST API (Gitea's is close to it: gitea embeds
// the shared parts).
type github struct {
	c    *client
	repo string // /repos/owner/name
}

type ghUser struct {
	Login string `json:"login"`
	Email string `json:"email"`
}

func (u *ghUser) user() User {
	if u == nil {
		return User{Login: "ghost"}
	}
	return User{Login: u.Login, Email: u.Email}
}

func ghUsers(us []ghUser) []User {
	out := make([]User, len(us))
	for i := range us {
		out[i] = us[i].user()
	}
	return out
}

type ghLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

func ghLabelNames(ls []ghLabel) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}

type ghMilestone struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	DueOn       *time.Time `json:"due_on"`
	ClosedAt    *time.Time `json:"closed_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (m *ghMilestone) title() string {
	if m == nil {
		return ""
	}
	return m.Title
}

type ghIssue struct {
	Number      int          `json:"number"`
	Title       string       `json:"title"`
	Body        string       `json:"body"`
	User        *ghUser      `json:"user"`
	State       string       `json:"state"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	ClosedAt    *time.Time   `json:"closed_at"`
	Labels      []ghLabel    `json:"labels"`
	Milestone   *ghMilestone `json:"milestone"`
	Assignees   []ghUser     `json:"assignees"`
	Comments    int          `json:"comments"`
	PullRequest *struct{}    `json:"pull_request"`
}

type ghComment struct {
	User         *ghUser   `json:"user"`
	Body         string    `json:"body"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Path         string    `json:"path"`
	Line         *int      `json:"line"`
	OriginalLine *int      `json:"original_line"`
	ReviewId     int64     `json:"pull_request_review_id"`
}

func (c ghComment) comment() Comment {
	out := Comment{Author: c.User.user(), Body: c.Body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Path: c.Path}
	if c.Line != nil {
		out.Line = *c.Line
	} else if c.OriginalLine != nil {
		out.Line = *c.OriginalLine
	}
	return out
}

type ghBranch struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
		Owner    ghUser `json:"owner"`
	} `json:"repo"`
}

type ghPull struct {
	Number         int          `json:"number"`
	Title          string       `json:"title"`
	Body           string       `json:"body"`
	User           *ghUser      `json:"user"`
	State          string       `json:"state"`
	Draft          bool         `json:"draft"`
	Merged         bool         `json:"merged"` // Gitea
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	ClosedAt       *time.Time   `json:"closed_at"`
	MergedAt       *time.Time   `json:"merged_at"`
	MergedBy       *ghUser      `json:"merged_by"`
	MergeCommitSHA string       `json:"merge_commit_sha"`
	Head           ghBranch     `json:"head"`
	Base           ghBranch     `json:"base"`
	Labels         []ghLabel    `json:"labels"`
	Milestone      *ghMilestone `json:"milestone"`
	Assignees      []ghUser     `json:"assignees"`
}

func (p ghPull) pull() Pull {
	out := Pull{Number: p.Number, Title: p.Title, Body: p.Body, Author: p.User.user(), State: PullOpen, Draft: p.Draft,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ClosedAt: p.ClosedAt, MergedAt: p.MergedAt,
		SourceBranch: p.Head.Ref, TargetBranch: p.Base.Ref, HeadSHA: p.Head.SHA, BaseSHA: p.Base.SHA,
		HeadRef: "refs/pull/" + strconv.Itoa(p.Number) + "/head", Labels: ghLabelNames(p.Labels), Milestone: p.Milestone.title(),
		Assignees: ghUsers(p.Assignees)}
	switch {
	case p.MergedAt != nil || p.Merged:
		out.State, out.MergeCommit = PullMerged, p.MergeCommitSHA
		if p.MergedBy != nil {
			u := p.MergedBy.user()
			out.MergedBy = &u
		}
	case p.State == "closed":
		out.State = PullClosed
	}
	if p.Head.Repo != nil && p.Base.Repo != nil && !strings.EqualFold(p.Head.Repo.FullName, p.Base.Repo.FullName) {
		out.SourceOwner = p.Head.Repo.Owner.Login
		if out.SourceOwner == "" {
			out.SourceOwner, _, _ = strings.Cut(p.Head.Repo.FullName, "/")
		}
	} else if p.Head.Repo == nil && p.Base.Repo != nil {
		out.SourceOwner = "deleted" // the fork is gone
	}
	return out
}

type ghReview struct {
	Id          int64     `json:"id"`
	User        *ghUser   `json:"user"`
	State       string    `json:"state"`
	Body        string    `json:"body"`
	CommitId    string    `json:"commit_id"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func verdict(state string) string {
	switch strings.ToUpper(state) {
	case "APPROVED":
		return Approved
	case "CHANGES_REQUESTED", "REQUEST_CHANGES":
		return ChangesRequested
	}
	return Commented
}

type ghAsset struct {
	Name               string `json:"name"`
	Label              string `json:"label"`
	ContentType        string `json:"content_type"`
	Size               int64  `json:"size"`
	URL                string `json:"url"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName         string     `json:"tag_name"`
	TargetCommitish string     `json:"target_commitish"`
	Name            string     `json:"name"`
	Body            string     `json:"body"`
	Draft           bool       `json:"draft"`
	Prerelease      bool       `json:"prerelease"`
	Author          *ghUser    `json:"author"`
	CreatedAt       time.Time  `json:"created_at"`
	PublishedAt     *time.Time `json:"published_at"`
	Assets          []ghAsset  `json:"assets"`
}

var allState = url.Values{"state": {"all"}, "per_page": {"100"}}

func (g *github) Kind() string { return GitHub }

func (g *github) MaxNumbers(ctx context.Context) (int, int, error) {
	// Issues include pull requests on GitHub: the newest one has the
	// highest number.
	var page []ghIssue
	if err := g.c.get(ctx, g.repo+"/issues", url.Values{"state": {"all"}, "per_page": {"1"}, "sort": {"created"}, "direction": {"desc"}}, &page); err != nil {
		return 0, 0, err
	}
	if len(page) == 0 {
		return 0, 0, nil
	}
	return page[0].Number, page[0].Number, nil
}

func (g *github) SharedNumbers() bool { return true }

func (g *github) Labels(ctx context.Context) ([]Label, error) {
	ls, err := all[ghLabel](ctx, g.c, g.repo+"/labels", url.Values{"per_page": {"100"}})
	out := make([]Label, len(ls))
	for i, l := range ls {
		out[i] = Label{Name: l.Name, Color: color(l.Color), Description: l.Description}
	}
	return out, err
}

func (g *github) Milestones(ctx context.Context) ([]Milestone, error) {
	ms, err := all[ghMilestone](ctx, g.c, g.repo+"/milestones", allState)
	out := make([]Milestone, len(ms))
	for i, m := range ms {
		out[i] = Milestone{Title: m.Title, Description: m.Description, Closed: m.State == "closed", DueOn: m.DueOn, ClosedAt: m.ClosedAt, CreatedAt: m.CreatedAt}
	}
	return out, err
}

func (g *github) Issues(ctx context.Context, fn func(Issue) error) error {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "sort": {"created"}, "direction": {"asc"}}
	return each(ctx, g.c, g.repo+"/issues", q, func(i ghIssue) error {
		if i.PullRequest != nil {
			return nil
		}
		return fn(ghToIssue(i))
	})
}

func ghToIssue(i ghIssue) Issue {
	return Issue{Number: i.Number, Title: i.Title, Body: i.Body, Author: i.User.user(), Closed: i.State == "closed",
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: i.ClosedAt, Labels: ghLabelNames(i.Labels),
		Milestone: i.Milestone.title(), Assignees: ghUsers(i.Assignees), Comments: i.Comments}
}

func (g *github) comments(ctx context.Context, path string) ([]Comment, error) {
	cs, err := all[ghComment](ctx, g.c, path, url.Values{"per_page": {"100"}})
	out := make([]Comment, len(cs))
	for i, c := range cs {
		out[i] = c.comment()
	}
	return out, err
}

func (g *github) IssueComments(ctx context.Context, number int) ([]Comment, error) {
	return g.comments(ctx, g.repo+"/issues/"+strconv.Itoa(number)+"/comments")
}

func (g *github) Pulls(ctx context.Context, fn func(Pull) error) error {
	q := url.Values{"state": {"all"}, "per_page": {"100"}, "sort": {"created"}, "direction": {"asc"}}
	return each(ctx, g.c, g.repo+"/pulls", q, func(p ghPull) error { return fn(p.pull()) })
}

func (g *github) PullComments(ctx context.Context, p Pull) ([]Comment, error) {
	return g.IssueComments(ctx, p.Number)
}

func (g *github) Reviews(ctx context.Context, p Pull) ([]Review, error) {
	n := strconv.Itoa(p.Number)
	rs, err := all[ghReview](ctx, g.c, g.repo+"/pulls/"+n+"/reviews", url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	inline, err := all[ghComment](ctx, g.c, g.repo+"/pulls/"+n+"/comments", url.Values{"per_page": {"100"}})
	if err != nil {
		return nil, err
	}
	byReview := map[int64][]Comment{}
	for _, c := range inline {
		byReview[c.ReviewId] = append(byReview[c.ReviewId], c.comment())
	}
	var out []Review
	for _, r := range rs {
		if strings.EqualFold(r.State, "PENDING") {
			continue
		}
		out = append(out, Review{Author: r.User.user(), State: verdict(r.State), Body: r.Body, CommitId: r.CommitId,
			SubmittedAt: r.SubmittedAt, Comments: byReview[r.Id]})
		delete(byReview, r.Id)
	}
	for _, cs := range byReview { // comments of reviews not listed
		for _, c := range cs {
			out = append(out, Review{Author: c.Author, State: Commented, SubmittedAt: c.CreatedAt, Comments: []Comment{c}})
		}
	}
	return out, nil
}

func ghToRelease(r ghRelease) Release {
	out := Release{TagName: r.TagName, Target: r.TargetCommitish, Name: r.Name, Body: r.Body, Draft: r.Draft, Prerelease: r.Prerelease,
		Author: r.Author.user(), CreatedAt: r.CreatedAt, PublishedAt: r.PublishedAt}
	for _, a := range r.Assets {
		out.Assets = append(out.Assets, Asset{Name: a.Name, Label: a.Label, ContentType: a.ContentType, Size: a.Size, URL: a.URL})
	}
	return out
}

func (g *github) Releases(ctx context.Context) ([]Release, error) {
	rs, err := all[ghRelease](ctx, g.c, g.repo+"/releases", url.Values{"per_page": {"100"}})
	out := make([]Release, len(rs))
	for i, r := range rs {
		out[i] = ghToRelease(r)
	}
	return out, err
}

// Download: the asset's API URL, as octet-stream (GitHub redirects to its
// storage, without our credentials).
func (g *github) Download(ctx context.Context, a Asset) (io.ReadCloser, error) {
	return g.c.download(ctx, a.URL, "application/octet-stream")
}

func (g *github) PullRefs(ref string) bool { return pullHeadRef(ref, "refs/pull/") }

// pullHeadRef: <prefix><number>/head.
func pullHeadRef(ref, prefix string) bool {
	n, ok := strings.CutPrefix(ref, prefix)
	if !ok {
		return false
	}
	n, ok = strings.CutSuffix(n, "/head")
	if !ok {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}
