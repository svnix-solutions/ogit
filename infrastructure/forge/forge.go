// Package forge reads a repository's issues, pull requests, labels,
// milestones and releases from GitHub (and GitHub Enterprise), GitLab and
// Gitea/Forgejo, into one model, for imports.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Kinds of forges.
const (
	GitHub = "github"
	GitLab = "gitlab"
	Gitea  = "gitea"
)

// Name is a forge's display name.
func Name(kind string) string {
	switch kind {
	case GitHub:
		return "GitHub"
	case GitLab:
		return "GitLab"
	case Gitea:
		return "Gitea"
	}
	return kind
}

// Valid reports whether kind is a known forge.
func Valid(kind string) bool { return kind == GitHub || kind == GitLab || kind == Gitea }

type User struct {
	Login string
	Email string // when the forge shows it
}

type Label struct {
	Name, Color, Description string // Color: #rrggbb
}

type Milestone struct {
	Title, Description string
	Closed             bool
	DueOn, ClosedAt    *time.Time
	CreatedAt          time.Time
}

type Comment struct {
	Author    User
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
	// Path and Line: an inline review comment's place (Line 0: unknown).
	Path string
	Line int
}

type Issue struct {
	Number    int
	Title     string
	Body      string
	Author    User
	Closed    bool
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	Labels    []string
	Milestone string // title
	Assignees []User
	Comments  int // how many; IssueComments fetches them
}

// Review verdicts.
const (
	Approved         = "approved"
	ChangesRequested = "changes_requested"
	Commented        = "commented"
)

type Review struct {
	Author      User
	State       string // Approved, ChangesRequested or Commented
	Body        string
	CommitId    string
	SubmittedAt time.Time
	Comments    []Comment // inline
}

// Pull request states.
const (
	PullOpen   = "open"
	PullClosed = "closed"
	PullMerged = "merged"
)

type Pull struct {
	Number    int // GitLab: the merge request's iid
	Title     string
	Body      string
	Author    User
	State     string // PullOpen, PullClosed or PullMerged
	Draft     bool
	CreatedAt time.Time
	UpdatedAt time.Time
	ClosedAt  *time.Time
	MergedAt  *time.Time
	MergedBy  *User

	SourceBranch string
	// SourceOwner: the fork the source branch is in ("" for this
	// repository).
	SourceOwner  string
	TargetBranch string
	HeadSHA      string
	BaseSHA      string
	MergeCommit  string
	// HeadRef: the git ref holding the head on the remote (fetched with
	// the branches: refs/pull/<n>/head, refs/merge-requests/<iid>/head).
	HeadRef string

	Labels    []string
	Milestone string
	Assignees []User
}

type Asset struct {
	Name, Label, ContentType string
	Size                     int64 // -1: unknown
	// URL downloads it (with the API's credentials when on the API's
	// host).
	URL string
}

type Release struct {
	TagName, Target, Name, Body string
	Draft, Prerelease           bool
	Author                      User
	CreatedAt                   time.Time
	PublishedAt                 *time.Time
	Assets                      []Asset
	// Links: assets that are only links (GitLab), kept in the notes.
	Links []Asset
}

// Source is a repository on a forge.
type Source interface {
	Kind() string
	// MaxNumbers are the highest issue and pull request numbers.
	MaxNumbers(ctx context.Context) (issues, pulls int, err error)
	// SharedNumbers: issues and pull requests share one sequence (GitHub,
	// Gitea); otherwise (GitLab) pull requests are numbered after the
	// issues here.
	SharedNumbers() bool
	Labels(ctx context.Context) ([]Label, error)
	Milestones(ctx context.Context) ([]Milestone, error)
	// Issues calls fn for each issue (not pull requests), oldest first.
	Issues(ctx context.Context, fn func(Issue) error) error
	IssueComments(ctx context.Context, number int) ([]Comment, error)
	// Pulls calls fn for each pull request, oldest first.
	Pulls(ctx context.Context, fn func(Pull) error) error
	PullComments(ctx context.Context, p Pull) ([]Comment, error)
	Reviews(ctx context.Context, p Pull) ([]Review, error)
	Releases(ctx context.Context) ([]Release, error)
	// Download opens an asset (the caller closes it).
	Download(ctx context.Context, a Asset) (io.ReadCloser, error)
	// PullRefs reports whether a remote git ref holds a pull request's
	// head (fetched with the branches).
	PullRefs(ref string) bool
}

// Options say where the forge is.
type Options struct {
	Kind string
	// APIURL: the API's base (https://api.github.com,
	// https://host/api/v3, https://host/api/v4, https://host/api/v1);
	// empty: derived from the remote URL.
	APIURL string
	// Project: owner/name (GitLab: the full path, groups included).
	Project string
	Token   string
	Client  *http.Client
}

// ErrRateLimited: the forge asks to wait until Until.
type ErrRateLimited struct{ Until time.Time }

func (e *ErrRateLimited) Error() string {
	return "the forge's API rate limit was reached; the import continues at " + e.Until.UTC().Format(time.RFC3339)
}

var (
	ErrUnauthorized = errors.New("the forge refused the API token (set an API token that can read the repository)")
	ErrNotFound     = errors.New("the repository was not found on the forge's API (private? set an API token)")
)

// New returns the source the options describe.
func New(o Options) (Source, error) {
	if !Valid(o.Kind) {
		return nil, fmt.Errorf("unknown forge %q", o.Kind)
	}
	project := strings.Trim(strings.TrimSuffix(strings.TrimSpace(o.Project), ".git"), "/")
	if project == "" || !strings.Contains(project, "/") {
		return nil, errors.New("the remote URL doesn't name a repository (owner/name)")
	}
	base, err := url.Parse(strings.TrimSuffix(o.APIURL, "/"))
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, errors.New("invalid API URL")
	}
	c := &client{base: base, token: o.Token, kind: o.Kind}
	c.http = c.stripping(o.Client)
	switch o.Kind {
	case GitHub:
		return &github{c: c, repo: "/repos/" + project}, nil
	case GitLab:
		return &gitlab{c: c, project: "/projects/" + url.PathEscape(project)}, nil
	default:
		return &gitea{c: c, repo: "/repos/" + project}, nil
	}
}

// APIURL derives a forge's API base and the project from a git remote
// (https://host/owner/name.git or git@host:owner/name.git).
func APIURL(kind, remote string) (api, project string, err error) {
	remote = strings.TrimSpace(remote)
	var scheme, host, path string
	if u, perr := url.Parse(remote); perr == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh") {
		scheme, host, path = u.Scheme, u.Host, u.Path
		if scheme == "ssh" {
			scheme, host = "https", u.Hostname()
		}
	} else if m := scpLike.FindStringSubmatch(remote); m != nil {
		scheme, host, path = "https", m[1], m[2]
	} else {
		return "", "", errors.New("can't tell the forge's address from the remote URL: set the API URL")
	}
	project = strings.Trim(strings.TrimSuffix(strings.Trim(path, "/"), ".git"), "/")
	switch kind {
	case GitHub:
		if host == "github.com" || host == "www.github.com" {
			return "https://api.github.com", project, nil
		}
		return scheme + "://" + host + "/api/v3", project, nil
	case GitLab:
		return scheme + "://" + host + "/api/v4", project, nil
	case Gitea:
		return scheme + "://" + host + "/api/v1", project, nil
	}
	return "", "", fmt.Errorf("unknown forge %q", kind)
}

var scpLike = regexp.MustCompile(`^[^@/:]+@([^:/]+):(.+)$`)

// Guess names the forge of well-known hosts ("" otherwise).
func Guess(remote string) string {
	switch {
	case strings.Contains(remote, "github.com"):
		return GitHub
	case strings.Contains(remote, "gitlab.com"):
		return GitLab
	case strings.Contains(remote, "codeberg.org"), strings.Contains(remote, "gitea.com"):
		return Gitea
	}
	return ""
}

// client calls a forge's JSON API.
type client struct {
	base  *url.URL
	token string
	http  *http.Client
	kind  string
}

const pageLimit = 1000 // pages of one listing, at most (100 000 items)

// url is the API's base plus path (already escaped) and query.
func (c *client) url(path string, query url.Values) string {
	href := strings.TrimSuffix(c.base.String(), "/") + path
	if len(query) > 0 {
		href += "?" + query.Encode()
	}
	return href
}

func (c *client) request(ctx context.Context, href string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.kind == GitHub {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	if c.token != "" && c.sameHost(req.URL) {
		switch c.kind {
		case GitLab:
			req.Header.Set("PRIVATE-TOKEN", c.token)
		case Gitea:
			req.Header.Set("Authorization", "token "+c.token)
		default:
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if until, ok := rateLimited(resp); ok {
		return nil, &ErrRateLimited{Until: until}
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrNotFound
	}
	return nil, fmt.Errorf("%s answered %s for %s", Name(c.kind), resp.Status, req.URL.Path)
}

// stripping copies hc so that redirects away from the API's host (and
// port: the HTTP client only compares domains) drop the credentials.
func (c *client) stripping(hc *http.Client) *http.Client {
	out := &http.Client{}
	if hc != nil {
		*out = *hc
	}
	next := out.CheckRedirect
	out.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !c.sameHost(req.URL) {
			req.Header.Del("Authorization")
			req.Header.Del("PRIVATE-TOKEN")
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return out
}

// sameHost: credentials only go to the API's host.
func (c *client) sameHost(u *url.URL) bool { return strings.EqualFold(u.Host, c.base.Host) }

// rateLimited reads GitHub's, GitLab's and Gitea's rate limit answers.
func rateLimited(resp *http.Response) (time.Time, bool) {
	if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusForbidden {
		return time.Time{}, false
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return time.Now().Add(time.Duration(n) * time.Second), true
		}
	}
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	if remaining == "" {
		remaining = resp.Header.Get("RateLimit-Remaining")
	}
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		reset = resp.Header.Get("RateLimit-Reset")
	}
	if remaining == "0" || resp.StatusCode == http.StatusTooManyRequests {
		if n, err := strconv.ParseInt(reset, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0), true
		}
		return time.Now().Add(time.Minute), true
	}
	return time.Time{}, false
}

// get decodes one answer.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.request(ctx, c.url(path, query))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// each pages through a listing (the Link header's next), decoding each
// page's items with decode.
func each[T any](ctx context.Context, c *client, path string, query url.Values, fn func(T) error) error {
	href := c.url(path, query)
	for range pageLimit {
		resp, err := c.request(ctx, href)
		if err != nil {
			return err
		}
		var page []T
		err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		for _, item := range page {
			if err := fn(item); err != nil {
				return err
			}
		}
		next := nextLink(resp.Header.Get("Link"))
		if next == "" || len(page) == 0 {
			return nil
		}
		u, err := url.Parse(next)
		if err != nil || !c.sameHost(u) {
			return fmt.Errorf("%s gave a next page on another host", Name(c.kind))
		}
		href = next
	}
	return nil
}

// all collects a listing.
func all[T any](ctx context.Context, c *client, path string, query url.Values) ([]T, error) {
	var out []T
	err := each(ctx, c, path, query, func(t T) error { out = append(out, t); return nil })
	return out, err
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		if m := linkNext.FindStringSubmatch(part); m != nil {
			return m[1]
		}
	}
	return ""
}

// download opens a file; credentials only go to the API's host (and the
// HTTP client drops them on redirects elsewhere).
func (c *client) download(ctx context.Context, href string, accept string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if c.token != "" && c.sameHost(req.URL) {
		switch c.kind {
		case GitLab:
			req.Header.Set("PRIVATE-TOKEN", c.token)
		case Gitea:
			req.Header.Set("Authorization", "token "+c.token)
		default:
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: %s", req.URL.Path, resp.Status)
	}
	return resp.Body, nil
}

func color(c string) string {
	c = strings.TrimPrefix(strings.TrimSpace(c), "#")
	if len(c) != 6 {
		return "#cccccc"
	}
	if _, err := strconv.ParseUint(c, 16, 32); err != nil {
		return "#cccccc"
	}
	return "#" + strings.ToLower(c)
}

// PullRefs returns a matcher for the git refs holding a forge's pull
// request heads (fetched with the branches before an import).
func PullRefs(kind string) func(ref string) bool {
	prefix := "refs/pull/"
	if kind == GitLab {
		prefix = "refs/merge-requests/"
	}
	return func(ref string) bool { return pullHeadRef(ref, prefix) }
}
