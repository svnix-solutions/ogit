package application

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/rs/zerolog"
)

// Commit status rules (GitHub's commit statuses):
//   - developers and up post statuses, with a session or a personal access
//     token that has write:status (posts statuses, can't push) or
//     write:repository; anyone who can read the repository reads them (a
//     token needs read:repository, write:repository or write:status);
//   - a status is for a full commit hash that exists in the repository,
//     with a state (pending, success, failure, error), a context ("default"
//     when empty), a description (cut to 140 characters) and an optional
//     http(s) target_url. Every report is kept; the latest per context
//     counts, and the combined state is failure > pending > success;
//   - branch protection rules can require contexts to be success on a pull
//     request's head before it is merged;
//   - each new status is a "status" webhook event.

var (
	ErrInvalidStatus  = errors.New("a status needs a state (pending, success, failure or error), a context of at most 255 characters, and an http(s) target_url if any")
	ErrCommitNotFound = errors.New("no commit found for this hash")
	ErrTokenScope     = errors.New("this token's scopes don't allow it")
	ErrTooManyCommits = errors.New("at most 100 commits at a time")
	ErrChecksRequired = errors.New("required checks are not passing")
)

const maxDescription = 140

type StatusApp struct {
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Statuses     domain.CommitStatusPers
	Webhooks     *WebhookApp
	// Failed is told about failing statuses (PullRequestApp.StatusFailed
	// notifies the authors of pull requests at that commit); nil: nobody.
	Failed func(repo *domain.Repository, st storer.Storer, sha string, s *domain.CommitStatus)
}

// StatusCaller is who calls: a user with a session (Token nil), or a
// personal access token (UserId is its user), or anonymous ("" and nil).
type StatusCaller struct {
	UserId string
	Token  *domain.Token
}

func (c StatusCaller) tokenAllows(scopes ...domain.TokenScope) bool {
	if c.Token == nil {
		return true
	}
	for _, s := range scopes {
		if slices.Contains(c.Token.Scopes, s) {
			return true
		}
	}
	return false
}

// StatusInput is a status to post.
type StatusInput struct {
	State       string
	Context     string
	Description string
	TargetURL   string
}

func (c *StatusApp) open(id, path *string, caller StatusCaller) (*domain.Repository, storer.Storer, error) {
	if !caller.tokenAllows(domain.ScopeReadRepository, domain.ScopeWriteRepository, domain.ScopeWriteStatus) {
		return nil, nil, ErrTokenScope
	}
	repo, err := c.Repositories.GetRepositoryForUser(id, path, caller.UserId)
	if err != nil {
		return nil, nil, err
	}
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return repo, st, nil
}

// Create posts a status on the commit sha.
func (c *StatusApp) Create(id, path *string, caller StatusCaller, sha string, in StatusInput) (*domain.CommitStatus, error) {
	repo, st, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	if caller.UserId == "" {
		return nil, ErrForbidden
	}
	if domain.IsPerson(caller.UserId) {
		role, err := c.Repositories.GetUserEffectiveRole(repo.Id, caller.UserId)
		if err != nil {
			return nil, err
		}
		if !role.CanWrite() {
			return nil, ErrForbidden
		}
	} else if caller.Token == nil || caller.Token.RepositoryId == nil || *caller.Token.RepositoryId != repo.Id {
		return nil, ErrForbidden // a repository token posts on its own repository only
	}
	if !caller.tokenAllows(domain.ScopeWriteStatus, domain.ScopeWriteRepository) {
		return nil, ErrTokenScope
	}
	if repo.IsArchived {
		return nil, ErrGitArchived
	}
	status, err := validStatus(in)
	if err != nil {
		return nil, err
	}
	sha = strings.ToLower(sha)
	if len(sha) != 40 || !plumbing.IsHash(sha) {
		return nil, ErrCommitNotFound
	}
	commit, err := gitbrowse.ResolveRef(st, sha)
	if err != nil || commit.Hash.String() != sha {
		return nil, ErrCommitNotFound
	}
	status.RepositoryId, status.SHA = repo.Id, sha
	if domain.IsPerson(caller.UserId) {
		status.CreatorId = &caller.UserId
	}
	if err := c.Statuses.Create(status); err != nil {
		return nil, err
	}
	sender := ""
	if status.CreatorId != nil {
		if u, err := c.Repositories.UserPers.GetById(caller.UserId); err == nil {
			status.Creator = &u
		}
		sender = caller.UserId
	}
	c.Webhooks.StatusEvent(repo, st, commit, status, sender)
	if c.Failed != nil && (status.State == domain.StatusFailure || status.State == domain.StatusError) {
		c.Failed(repo, st, sha, status)
	}
	return status, nil
}

func resolveCommit(st storer.Storer, ref string) (*object.Commit, error) {
	commit, err := gitbrowse.ResolveRef(st, ref)
	if errors.Is(err, gitbrowse.ErrRefNotFound) || errors.Is(err, gitbrowse.ErrEmpty) {
		return nil, ErrCommitNotFound
	}
	return commit, err
}

func validStatus(in StatusInput) (*domain.CommitStatus, error) {
	s := &domain.CommitStatus{State: domain.StatusState(in.State), Context: strings.TrimSpace(in.Context), TargetURL: strings.TrimSpace(in.TargetURL)}
	if s.Context == "" {
		s.Context = "default"
	}
	if !s.State.Valid() || len(s.Context) > 255 || len(s.TargetURL) > 2000 {
		return nil, ErrInvalidStatus
	}
	// Shown as a link in the UI: only http(s), never javascript: or data:.
	if s.TargetURL != "" {
		u, err := url.Parse(s.TargetURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, ErrInvalidStatus
		}
	}
	s.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(s.Description) > maxDescription {
		s.Description = string([]rune(s.Description)[:maxDescription-1]) + "…"
	}
	return s, nil
}

// List returns the statuses of a commit (or ref), newest first.
func (c *StatusApp) List(id, path *string, caller StatusCaller, ref string) (*domain.Repository, string, []domain.CommitStatus, error) {
	repo, st, err := c.open(id, path, caller)
	if err != nil {
		return nil, "", nil, err
	}
	commit, err := resolveCommit(st, ref)
	if err != nil {
		return nil, "", nil, err
	}
	statuses, err := c.Statuses.List(repo.Id, commit.Hash.String(), 100)
	return repo, commit.Hash.String(), statuses, err
}

// Combined is a commit's latest status per context and their combined
// state.
type Combined struct {
	SHA      string
	State    domain.StatusState
	Statuses []domain.CommitStatus // latest per context, by context
}

func combine(sha string, latest []domain.CommitStatus) Combined {
	slices.SortFunc(latest, func(a, b domain.CommitStatus) int { return strings.Compare(a.Context, b.Context) })
	if latest == nil {
		latest = []domain.CommitStatus{}
	}
	return Combined{SHA: sha, State: domain.CombinedState(latest), Statuses: latest}
}

// CombinedFor returns the combined status of a commit (or ref).
func (c *StatusApp) CombinedFor(id, path *string, caller StatusCaller, ref string) (*domain.Repository, *Combined, error) {
	repo, st, err := c.open(id, path, caller)
	if err != nil {
		return nil, nil, err
	}
	commit, err := resolveCommit(st, ref)
	if err != nil {
		return nil, nil, err
	}
	sha := commit.Hash.String()
	latest, err := c.Statuses.Latest(repo.Id, []string{sha})
	if err != nil {
		return nil, nil, err
	}
	out := combine(sha, latest[sha])
	return repo, &out, nil
}

// Batch returns the combined status of up to 100 commits (full hashes) for
// the UI's icons. Commits without statuses are left out.
func (c *StatusApp) Batch(id, path *string, caller StatusCaller, shas []string) (map[string]Combined, error) {
	if len(shas) > 100 {
		return nil, ErrTooManyCommits
	}
	repo, _, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	latest, err := c.Statuses.Latest(repo.Id, shas)
	if err != nil {
		return nil, err
	}
	out := map[string]Combined{}
	for sha, l := range latest {
		out[sha] = combine(sha, l)
	}
	return out, nil
}

// RecentContexts returns the contexts reported in the last 30 days (for
// picking required checks).
func (c *StatusApp) RecentContexts(id, path *string, caller StatusCaller) ([]string, error) {
	repo, _, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	out, err := c.Statuses.Contexts(repo.Id, time.Now().AddDate(0, 0, -30), 50)
	if out == nil {
		out = []string{}
	}
	return out, err
}

// RequiredChecks reports, for each required context, its latest state on
// sha ("" when never reported).
func (c *StatusApp) RequiredChecks(repo *domain.Repository, sha string, required []string) (map[string]domain.StatusState, error) {
	out := map[string]domain.StatusState{}
	if c == nil || len(required) == 0 {
		return out, nil
	}
	latest, err := c.Statuses.Latest(repo.Id, []string{sha})
	if err != nil {
		return nil, err
	}
	for _, ctx := range required {
		out[ctx] = ""
	}
	for _, s := range latest[sha] {
		if _, ok := out[s.Context]; ok {
			out[s.Context] = s.State
		}
	}
	return out, nil
}
