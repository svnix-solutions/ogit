package forge

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strconv"
	"time"
)

// gitlab reads GitLab's v4 API. Issues and merge requests have separate
// numbers (iid); release assets are links.
type gitlab struct {
	c       *client
	project string // /projects/<escaped path>
}

type glUser struct {
	Username    string `json:"username"`
	PublicEmail string `json:"public_email"`
}

func (u *glUser) user() User {
	if u == nil {
		return User{Login: "ghost"}
	}
	return User{Login: u.Username, Email: u.PublicEmail}
}

func glUsers(us []glUser) []User {
	out := make([]User, len(us))
	for i := range us {
		out[i] = us[i].user()
	}
	return out
}

type glMilestone struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	DueDate     string    `json:"due_date"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (m *glMilestone) title() string {
	if m == nil {
		return ""
	}
	return m.Title
}

type glIssue struct {
	IID         int          `json:"iid"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Author      *glUser      `json:"author"`
	State       string       `json:"state"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	ClosedAt    *time.Time   `json:"closed_at"`
	Labels      []string     `json:"labels"`
	Milestone   *glMilestone `json:"milestone"`
	Assignees   []glUser     `json:"assignees"`
	Notes       int          `json:"user_notes_count"`
}

type glNote struct {
	Body      string    `json:"body"`
	Author    *glUser   `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	System    bool      `json:"system"`
	Type      string    `json:"type"`
	Position  *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine *int   `json:"new_line"`
		OldLine *int   `json:"old_line"`
	} `json:"position"`
}

func (n glNote) comment() Comment {
	c := Comment{Author: n.Author.user(), Body: n.Body, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
	if p := n.Position; p != nil {
		c.Path = p.NewPath
		if c.Path == "" {
			c.Path = p.OldPath
		}
		if p.NewLine != nil {
			c.Line = *p.NewLine
		} else if p.OldLine != nil {
			c.Line = *p.OldLine
		}
	}
	return c
}

type glMR struct {
	IID             int          `json:"iid"`
	Title           string       `json:"title"`
	Description     string       `json:"description"`
	Author          *glUser      `json:"author"`
	State           string       `json:"state"`
	Draft           bool         `json:"draft"`
	CreatedAt       time.Time    `json:"created_at"`
	UpdatedAt       time.Time    `json:"updated_at"`
	ClosedAt        *time.Time   `json:"closed_at"`
	MergedAt        *time.Time   `json:"merged_at"`
	MergedBy        *glUser      `json:"merged_by"`
	MergeUser       *glUser      `json:"merge_user"`
	MergeCommitSHA  string       `json:"merge_commit_sha"`
	SquashCommitSHA string       `json:"squash_commit_sha"`
	SourceBranch    string       `json:"source_branch"`
	TargetBranch    string       `json:"target_branch"`
	SourceProjectId int64        `json:"source_project_id"`
	TargetProjectId int64        `json:"target_project_id"`
	SHA             string       `json:"sha"`
	Labels          []string     `json:"labels"`
	Milestone       *glMilestone `json:"milestone"`
	Assignees       []glUser     `json:"assignees"`
	DiffRefs        *struct {
		BaseSHA string `json:"base_sha"`
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

type glRelease struct {
	TagName     string     `json:"tag_name"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	Upcoming    bool       `json:"upcoming_release"`
	Author      *glUser    `json:"author"`
	Commit      *struct {
		Id string `json:"id"`
	} `json:"commit"`
	Assets struct {
		Links []struct {
			Name           string `json:"name"`
			URL            string `json:"url"`
			DirectAssetURL string `json:"direct_asset_url"`
		} `json:"links"`
	} `json:"assets"`
}

var glPage = url.Values{"per_page": {"100"}}

func glAll(extra url.Values) url.Values {
	q := url.Values{"per_page": {"100"}}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

func (g *gitlab) Kind() string { return GitLab }

func (g *gitlab) MaxNumbers(ctx context.Context) (int, int, error) {
	newest := url.Values{"scope": {"all"}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"desc"}, "per_page": {"1"}}
	var issues []glIssue
	if err := g.c.get(ctx, g.project+"/issues", newest, &issues); err != nil {
		return 0, 0, err
	}
	var mrs []glMR
	if err := g.c.get(ctx, g.project+"/merge_requests", newest, &mrs); err != nil {
		return 0, 0, err
	}
	i, m := 0, 0
	if len(issues) > 0 {
		i = issues[0].IID
	}
	if len(mrs) > 0 {
		m = mrs[0].IID
	}
	return i, m, nil
}

func (g *gitlab) SharedNumbers() bool { return false }

func (g *gitlab) Labels(ctx context.Context) ([]Label, error) {
	ls, err := all[ghLabel](ctx, g.c, g.project+"/labels", glPage)
	out := make([]Label, len(ls))
	for i, l := range ls {
		out[i] = Label{Name: l.Name, Color: color(l.Color), Description: l.Description}
	}
	return out, err
}

func (g *gitlab) Milestones(ctx context.Context) ([]Milestone, error) {
	ms, err := all[glMilestone](ctx, g.c, g.project+"/milestones", glPage)
	out := make([]Milestone, len(ms))
	for i, m := range ms {
		out[i] = Milestone{Title: m.Title, Description: m.Description, Closed: m.State == "closed", CreatedAt: m.CreatedAt}
		if d, err := time.Parse("2006-01-02", m.DueDate); err == nil {
			out[i].DueOn = &d
		}
		if out[i].Closed {
			closed := m.UpdatedAt
			out[i].ClosedAt = &closed
		}
	}
	return out, err
}

func (g *gitlab) Issues(ctx context.Context, fn func(Issue) error) error {
	q := glAll(url.Values{"scope": {"all"}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"asc"}})
	return each(ctx, g.c, g.project+"/issues", q, func(i glIssue) error {
		return fn(Issue{Number: i.IID, Title: i.Title, Body: i.Description, Author: i.Author.user(), Closed: i.State == "closed",
			CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: i.ClosedAt, Labels: i.Labels, Milestone: i.Milestone.title(),
			Assignees: glUsers(i.Assignees), Comments: i.Notes})
	})
}

// notes lists a thread's notes, without the system ones ("changed the
// title"...).
func (g *gitlab) notes(ctx context.Context, path string) ([]glNote, error) {
	ns, err := all[glNote](ctx, g.c, path, glAll(url.Values{"sort": {"asc"}, "order_by": {"created_at"}}))
	out := ns[:0]
	for _, n := range ns {
		if !n.System {
			out = append(out, n)
		}
	}
	return out, err
}

func (g *gitlab) IssueComments(ctx context.Context, number int) ([]Comment, error) {
	ns, err := g.notes(ctx, g.project+"/issues/"+strconv.Itoa(number)+"/notes")
	out := make([]Comment, len(ns))
	for i, n := range ns {
		out[i] = n.comment()
	}
	return out, err
}

func (g *gitlab) Pulls(ctx context.Context, fn func(Pull) error) error {
	q := glAll(url.Values{"scope": {"all"}, "state": {"all"}, "order_by": {"created_at"}, "sort": {"asc"}})
	return each(ctx, g.c, g.project+"/merge_requests", q, func(m glMR) error {
		// The listing has no diff_refs (the base).
		var full glMR
		if err := g.c.get(ctx, g.project+"/merge_requests/"+strconv.Itoa(m.IID), nil, &full); err == nil {
			m.DiffRefs = full.DiffRefs
		} else if errors.As(err, new(*ErrRateLimited)) {
			return err
		}
		return fn(m.pull())
	})
}

func (m glMR) pull() Pull {
	p := Pull{Number: m.IID, Title: m.Title, Body: m.Description, Author: m.Author.user(), State: PullOpen, Draft: m.Draft,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, ClosedAt: m.ClosedAt, MergedAt: m.MergedAt,
		SourceBranch: m.SourceBranch, TargetBranch: m.TargetBranch, HeadSHA: m.SHA,
		HeadRef: "refs/merge-requests/" + strconv.Itoa(m.IID) + "/head", Labels: m.Labels, Milestone: m.Milestone.title(), Assignees: glUsers(m.Assignees)}
	if m.DiffRefs != nil {
		p.BaseSHA = m.DiffRefs.BaseSHA
		if m.DiffRefs.HeadSHA != "" {
			p.HeadSHA = m.DiffRefs.HeadSHA
		}
	}
	switch m.State {
	case "merged":
		p.State, p.MergeCommit = PullMerged, m.MergeCommitSHA
		if p.MergeCommit == "" {
			p.MergeCommit = m.SquashCommitSHA
		}
		by := m.MergeUser
		if by == nil {
			by = m.MergedBy
		}
		if by != nil {
			u := by.user()
			p.MergedBy = &u
		}
	case "closed", "locked":
		p.State = PullClosed
	}
	if m.SourceProjectId != 0 && m.SourceProjectId != m.TargetProjectId {
		p.SourceOwner = "fork"
	}
	return p
}

func (g *gitlab) PullComments(ctx context.Context, p Pull) ([]Comment, error) {
	ns, err := g.notes(ctx, g.project+"/merge_requests/"+strconv.Itoa(p.Number)+"/notes")
	var out []Comment
	for _, n := range ns {
		if n.Type != "DiffNote" {
			out = append(out, n.comment())
		}
	}
	return out, err
}

// Reviews: approvals, and diff notes as comment reviews.
func (g *gitlab) Reviews(ctx context.Context, p Pull) ([]Review, error) {
	n := strconv.Itoa(p.Number)
	var approvals struct {
		ApprovedBy []struct {
			User glUser `json:"user"`
		} `json:"approved_by"`
	}
	var out []Review
	if err := g.c.get(ctx, g.project+"/merge_requests/"+n+"/approvals", nil, &approvals); err == nil {
		for _, a := range approvals.ApprovedBy {
			out = append(out, Review{Author: a.User.user(), State: Approved, CommitId: p.HeadSHA, SubmittedAt: p.UpdatedAt})
		}
	} else if errors.As(err, new(*ErrRateLimited)) {
		return nil, err
	}
	ns, err := g.notes(ctx, g.project+"/merge_requests/"+n+"/notes")
	if err != nil {
		return nil, err
	}
	for _, note := range ns {
		if note.Type == "DiffNote" {
			c := note.comment()
			out = append(out, Review{Author: c.Author, State: Commented, SubmittedAt: c.CreatedAt, Comments: []Comment{c}})
		}
	}
	return out, nil
}

func (g *gitlab) Releases(ctx context.Context) ([]Release, error) {
	rs, err := all[glRelease](ctx, g.c, g.project+"/releases", glPage)
	out := make([]Release, len(rs))
	for i, r := range rs {
		out[i] = Release{TagName: r.TagName, Name: r.Name, Body: r.Description, Author: r.Author.user(), CreatedAt: r.CreatedAt,
			PublishedAt: r.ReleasedAt, Prerelease: r.Upcoming}
		if r.Commit != nil {
			out[i].Target = r.Commit.Id
		}
		for _, l := range r.Assets.Links {
			href := l.DirectAssetURL
			if href == "" {
				href = l.URL
			}
			out[i].Links = append(out[i].Links, Asset{Name: l.Name, URL: href, Size: -1})
		}
	}
	return out, err
}

// Download: GitLab's assets are links, kept in the notes.
func (g *gitlab) Download(ctx context.Context, a Asset) (io.ReadCloser, error) {
	return g.c.download(ctx, a.URL, "")
}

func (g *gitlab) PullRefs(ref string) bool { return pullHeadRef(ref, "refs/merge-requests/") }
