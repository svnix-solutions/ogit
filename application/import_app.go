package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/forge"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/labbs/ogit/infrastructure/mirror"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

// API import rules:
//   - an import (or a new mirror) may also copy the remote's issues, pull
//     requests, labels, milestones and releases (with their assets) from
//     its forge's API: GitHub (and GitHub Enterprise), GitLab or
//     Gitea/Forgejo; once, in the background, after the git data (which
//     brings the pull requests' heads along);
//   - the API token is the import's password unless given; it is sealed
//     and forgotten once the import is done; API calls go through netguard
//     (mirrors.allowed_hosts) like the git ones;
//   - numbers are kept: the forge's highest number is reserved first, so
//     issues opened here meanwhile come after; GitLab's merge requests,
//     numbered apart from its issues, come after the issues (with a note);
//   - authors, assignees and mergers are users with the same user name
//     (or email); others are the importer, with a line naming the original
//     author and date; reviews of unmatched reviewers are comments (an
//     approval must not count as the importer's);
//   - open pull requests stay open: mergeable when their source branch is
//     here, read-only (their recorded head) otherwise; pull requests from
//     forks keep "owner:branch" as their source, which no branch matches;
//   - each item is written in one transaction and skipped when already
//     there, so a stopped or failed import resumes; rate limits pause it
//     until the forge's reset; failures retry with backoff, 5 times;
//   - maintainers and owners see the progress, warnings (skipped assets)
//     and errors, and retry; owners cancel it. No notifications, webhooks
//     or Actions runs.

var (
	ErrImportNotFound = errors.New("this repository has no API import")
	ErrInvalidImport  = errors.New("invalid API import: choose github, gitlab or gitea, and an API URL if it can't be told from the remote URL")
)

const (
	importLock        = 10 * time.Minute
	importMaxFailures = 5
)

type ImportApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.ImportPers
	Repositories *RepositoryApp
	Users        domain.UserPers
	Issues       domain.IssuePers
	Pulls        domain.PullRequestPers
	Labels       domain.LabelPers
	Milestones   domain.MilestonePers
	Releases     domain.ReleasePers
	Assets       domain.ReleaseAssetPers
	Storage      storage.Config
	Box          *secretbox.Box
	Dialer       mirror.Dialer
	Now          func() time.Time
}

func (c *ImportApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// APIImportInput asks for an API import with an import.
type APIImportInput struct {
	Forge  string
	APIURL string // "": derived from the remote URL
	Token  *string
}

// prepare checks an API import of remote; the row is created with the
// repository.
func (c *ImportApp) prepare(in APIImportInput, remote, password, actorId string) (*domain.Import, error) {
	if c == nil || !forge.Valid(in.Forge) {
		return nil, ErrInvalidImport
	}
	api, project, err := forge.APIURL(in.Forge, remote)
	if err != nil {
		return nil, ErrInvalidImport
	}
	if u := strings.TrimSpace(in.APIURL); u != "" {
		api = u
	}
	if _, err := forge.New(forge.Options{Kind: in.Forge, APIURL: api, Project: project}); err != nil {
		return nil, ErrInvalidImport
	}
	token := password
	if in.Token != nil {
		token = *in.Token
	}
	secret := ""
	if token != "" {
		if c.Box == nil {
			return nil, ErrMirrorNeedsKey
		}
		if secret, err = c.Box.Seal(token); err != nil {
			return nil, err
		}
	}
	now := c.now()
	return &domain.Import{ActorId: actorId, Forge: in.Forge, APIURL: api, Project: project, Secret: secret, Status: domain.ImportWaiting,
		Stage: domain.StageLabels, NextRunAt: now, CreatedAt: now, UpdatedAt: now}, nil
}

// PullRefs: the remote refs a waiting import needs fetched with the
// branches (its pull requests' heads), or nil.
func (c *ImportApp) PullRefs(repo *domain.Repository) func(string) bool {
	if c == nil {
		return nil
	}
	imp, err := c.Pers.ForRepository(repo.Id)
	if err != nil || imp.Status == domain.ImportDone {
		return nil
	}
	return forge.PullRefs(imp.Forge)
}

// GitReady starts a waiting import once its git data is in.
func (c *ImportApp) GitReady(repo *domain.Repository) {
	if c == nil {
		return
	}
	if err := c.Pers.Ready(repo.Id, c.now()); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.import").Str("repository", repo.FullPath()).Msg("failed to start an import")
	}
}

// Forget drops a repository's import (its git import was cancelled).
func (c *ImportApp) Forget(repo *domain.Repository) {
	if c == nil {
		return
	}
	if imp, err := c.Pers.ForRepository(repo.Id); err == nil {
		_ = c.Pers.Delete(imp.Id)
	}
}

func (c *ImportApp) managed(id, path *string, actorId string) (*domain.Repository, *domain.Import, domain.AccessType, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, nil, role, err
	}
	if role.Level() < domain.AccessTypeMaintainer.Level() {
		return nil, nil, role, ErrForbidden
	}
	imp, err := c.Pers.ForRepository(repo.Id)
	if isNotFound(err) {
		return nil, nil, role, ErrImportNotFound
	}
	return repo, imp, role, err
}

// Get returns a repository's API import (maintainers and owners).
func (c *ImportApp) Get(id, path *string, actorId string) (*domain.Import, error) {
	_, imp, _, err := c.managed(id, path, actorId)
	return imp, err
}

// Retry resumes a failed import now.
func (c *ImportApp) Retry(id, path *string, actorId string) (*domain.Import, error) {
	_, imp, _, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if imp.Status != domain.ImportFailed {
		return nil, ErrMirrorInProgress
	}
	imp.Status, imp.NextRunAt, imp.Failures = domain.ImportPending, c.now(), 0
	return imp, c.Pers.Update(imp.Id, map[string]any{"status": imp.Status, "next_run_at": imp.NextRunAt, "failures": 0})
}

// Cancel stops an import (owners); what was imported stays.
func (c *ImportApp) Cancel(id, path *string, actorId string) error {
	repo, imp, role, err := c.managed(id, path, actorId)
	if err != nil {
		return err
	}
	if role != domain.AccessTypeOwner {
		return ErrForbidden
	}
	c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", "import.cancel").Str("repository", repo.FullPath()).Msg("API import cancelled")
	return c.Pers.Delete(imp.Id)
}

// Run imports due imports until ctx ends.
func (c *ImportApp) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for c.RunDue(ctx) > 0 {
			}
		}
	}
}

// RunDue runs one due import, if any (tests call it directly).
func (c *ImportApp) RunDue(ctx context.Context) int {
	imp, err := c.Pers.Claim(c.now(), c.now().Add(importLock))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.import").Msg("failed to claim an import")
		return 0
	}
	if imp == nil {
		return 0
	}
	c.run(ctx, imp)
	return 1
}

// importer runs one import.
type importer struct {
	c        *ImportApp
	imp      *domain.Import
	repo     *domain.Repository
	src      forge.Source
	st       storer.Storer
	users    map[string]*string // login -> user id (nil: none)
	labels   map[string]string  // lower(name) -> id
	mstones  map[string]string  // title -> id
	beat     time.Time
	warnings []string
}

func (c *ImportApp) run(ctx context.Context, imp *domain.Import) {
	repo, err := c.Repositories.GetRepository(&imp.RepositoryId, nil)
	if err != nil {
		_ = c.Pers.Delete(imp.Id)
		return
	}
	im := &importer{c: c, imp: imp, repo: repo, users: map[string]*string{}, beat: c.now()}
	if imp.Warnings != "" {
		im.warnings = strings.Split(imp.Warnings, "\n")
	}
	err = im.start(ctx)
	for err == nil && imp.Stage != domain.StageDone {
		switch imp.Stage {
		case domain.StageLabels:
			err = im.importLabels(ctx)
		case domain.StageMilestones:
			err = im.importMilestones(ctx)
		case domain.StageIssues:
			err = im.importIssues(ctx)
		case domain.StagePulls:
			err = im.importPulls(ctx)
		case domain.StageReleases:
			err = im.importReleases(ctx)
		}
		if err == nil {
			imp.Stage = nextStage(imp.Stage)
			err = im.save(map[string]any{"stage": imp.Stage})
		}
	}
	now := c.now()
	fields := map[string]any{"locked_until": nil, "updated_at": now, "warnings": strings.Join(im.warnings, "\n")}
	var limited *forge.ErrRateLimited
	switch {
	case err == nil:
		fields["status"], fields["finished_at"], fields["secret"], fields["last_error"] = domain.ImportDone, now, "", ""
		c.Logger.Info().Str("component", "application.import").Str("repository", repo.FullPath()).Int("issues", imp.Issues).Int("pulls", imp.Pulls).
			Int("releases", imp.Releases).Msg("API import done")
	case errors.As(err, &limited):
		fields["status"], fields["next_run_at"], fields["last_error"] = domain.ImportPending, limited.Until.UTC().Add(5*time.Second), err.Error()
	default:
		imp.Failures++
		fields["failures"], fields["last_error"] = imp.Failures, mirror.Describe(err)
		if imp.Failures >= importMaxFailures {
			fields["status"] = domain.ImportFailed
		} else {
			fields["status"], fields["next_run_at"] = domain.ImportPending, now.Add(mirrorRetry(imp.Failures, int((6*time.Hour).Seconds())))
		}
		c.Logger.Warn().Err(err).Str("component", "application.import").Str("repository", repo.FullPath()).Str("stage", imp.Stage).Msg("API import failed")
	}
	_ = c.Pers.Update(imp.Id, fields)
}

func nextStage(s string) string {
	order := []string{domain.StageLabels, domain.StageMilestones, domain.StageIssues, domain.StagePulls, domain.StageReleases, domain.StageDone}
	for i, o := range order[:len(order)-1] {
		if o == s {
			return order[i+1]
		}
	}
	return domain.StageDone
}

// start connects to the forge and reserves the numbers.
func (im *importer) start(ctx context.Context) error {
	token := ""
	if im.imp.Secret != "" {
		if im.c.Box == nil {
			return ErrMirrorNeedsKey
		}
		plain, err := im.c.Box.Open(im.imp.Secret)
		if err != nil {
			return err
		}
		token = plain
	}
	src, err := forge.New(forge.Options{Kind: im.imp.Forge, APIURL: im.imp.APIURL, Project: im.imp.Project, Token: token, Client: im.c.Dialer.HTTPClient()})
	if err != nil {
		return err
	}
	im.src = src
	if im.st, err = im.c.Storage.GetStorer(im.repo.StorageKey); err != nil {
		return err
	}
	if im.imp.Reserved == 0 && (im.imp.Stage == domain.StageLabels || im.imp.Stage == domain.StageMilestones) {
		issues, pulls, err := src.MaxNumbers(ctx)
		if err != nil {
			return err
		}
		reserved, offset := max(issues, pulls), 0
		if !src.SharedNumbers() {
			reserved, offset = issues+pulls, issues
		}
		if err := im.c.Pers.Reserve(im.repo.Id, reserved); err != nil {
			return err
		}
		im.imp.Reserved, im.imp.PullOffset = reserved, offset
		return im.save(map[string]any{"reserved": reserved, "pull_offset": offset})
	}
	return nil
}

// save records progress and keeps the lock.
func (im *importer) save(fields map[string]any) error {
	now := im.c.now()
	fields["updated_at"] = now
	if now.Sub(im.beat) > time.Minute {
		fields["locked_until"], im.beat = now.Add(importLock), now
	}
	return im.c.Pers.Update(im.imp.Id, fields)
}

func (im *importer) warn(format string, args ...any) {
	if len(im.warnings) < 100 {
		im.warnings = append(im.warnings, fmt.Sprintf(format, args...))
	}
}

// user maps a forge user to a user here (nil: none).
func (im *importer) user(u forge.User) *string {
	if u.Login == "" {
		return nil
	}
	key := strings.ToLower(u.Login)
	if id, ok := im.users[key]; ok {
		return id
	}
	var id *string
	if found, err := im.c.Users.GetByUsername(u.Login); err == nil && found.Id != "" {
		id = &found.Id
	} else if u.Email != "" {
		if found, err := im.c.Users.GetByEmail(u.Email); err == nil && found.Id != "" {
			id = &found.Id
		}
	}
	im.users[key] = id
	return id
}

// author maps an author: the user, or the importer and a note to start
// the body with.
func (im *importer) author(u forge.User, at time.Time) (*string, string) {
	if id := im.user(u); id != nil {
		return id, ""
	}
	actor := im.imp.ActorId
	return &actor, fmt.Sprintf("*Originally by @%s on %s, %s*\n\n", u.Login, forge.Name(im.imp.Forge), at.UTC().Format("2006-01-02"))
}

func (im *importer) assignees(us []forge.User) []string {
	var out []string
	seen := map[string]bool{}
	for _, u := range us {
		if id := im.user(u); id != nil && !seen[*id] {
			seen[*id] = true
			out = append(out, *id)
		}
	}
	return out
}

func (im *importer) labelIds(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		if id, ok := im.labels[strings.ToLower(n)]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func (im *importer) milestone(title string) *string {
	if id, ok := im.mstones[title]; ok {
		return &id
	}
	return nil
}

// loadLabels and loadMilestones read what's here (resumed imports skip
// the stages that create them).
func (im *importer) loadLabels() error {
	if im.labels != nil {
		return nil
	}
	ls, err := im.c.Labels.List(im.repo.Id)
	if err != nil {
		return err
	}
	im.labels = map[string]string{}
	for _, l := range ls {
		im.labels[strings.ToLower(l.Name)] = l.Id
	}
	return nil
}

func (im *importer) loadMilestones() error {
	if im.mstones != nil {
		return nil
	}
	ms, err := im.c.Milestones.List(im.repo.Id, "")
	if err != nil {
		return err
	}
	im.mstones = map[string]string{}
	for _, m := range ms {
		im.mstones[m.Title] = m.Id
	}
	return nil
}

func (im *importer) importLabels(ctx context.Context) error {
	if err := im.loadLabels(); err != nil {
		return err
	}
	ls, err := im.src.Labels(ctx)
	if err != nil {
		return err
	}
	for _, l := range ls {
		if _, ok := im.labels[strings.ToLower(l.Name)]; ok || strings.TrimSpace(l.Name) == "" {
			continue
		}
		label := &domain.Label{RepositoryId: im.repo.Id, Name: l.Name, Color: l.Color, Description: l.Description, CreatedAt: im.c.now()}
		if err := im.c.Labels.Create(label); err != nil {
			return err
		}
		im.labels[strings.ToLower(l.Name)] = label.Id
	}
	return nil
}

func (im *importer) importMilestones(ctx context.Context) error {
	if err := im.loadMilestones(); err != nil {
		return err
	}
	ms, err := im.src.Milestones(ctx)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, ok := im.mstones[m.Title]; ok || strings.TrimSpace(m.Title) == "" {
			continue
		}
		state := domain.MilestoneOpen
		if m.Closed {
			state = domain.MilestoneClosed
		}
		row := &domain.Milestone{RepositoryId: im.repo.Id, Title: m.Title, Description: m.Description, DueOn: m.DueOn, State: state,
			ClosedAt: m.ClosedAt, CreatedAt: m.CreatedAt, UpdatedAt: m.CreatedAt}
		if row.CreatedAt.IsZero() {
			row.CreatedAt, row.UpdatedAt = im.c.now(), im.c.now()
		}
		if err := im.c.Milestones.Create(row); err != nil {
			return err
		}
		im.mstones[m.Title] = row.Id
	}
	return nil
}

// number is an item's number here: kept when reserved, else the next
// free one (0) with a note.
func (im *importer) number(n int) (int, string) {
	if n > 0 && n <= im.imp.Reserved {
		return n, ""
	}
	return 0, fmt.Sprintf("*Originally #%d on %s*\n\n", n, forge.Name(im.imp.Forge))
}

func (im *importer) importIssues(ctx context.Context) error {
	if err := im.loadLabels(); err != nil {
		return err
	}
	if err := im.loadMilestones(); err != nil {
		return err
	}
	return im.src.Issues(ctx, func(i forge.Issue) error {
		n, numberNote := im.number(i.Number)
		if n > 0 {
			if _, err := im.c.Issues.Get(im.repo.Id, n); err == nil {
				return nil // imported already (a resumed import)
			}
		}
		var comments []domain.IssueComment
		if i.Comments > 0 {
			cs, err := im.src.IssueComments(ctx, i.Number)
			if err != nil {
				return err
			}
			for _, c := range cs {
				author, note := im.author(c.Author, c.CreatedAt)
				comments = append(comments, domain.IssueComment{AuthorId: author, Body: note + c.Body, CreatedAt: c.CreatedAt, UpdatedAt: later(c.UpdatedAt, c.CreatedAt)})
			}
		}
		author, note := im.author(i.Author, i.CreatedAt)
		issue := &domain.Issue{RepositoryId: im.repo.Id, Number: n, Title: title(i.Title), Body: numberNote + note + i.Body, AuthorId: author,
			State: domain.IssueOpen, MilestoneId: im.milestone(i.Milestone), CreatedAt: i.CreatedAt, UpdatedAt: later(i.UpdatedAt, i.CreatedAt)}
		if i.Closed {
			issue.State = domain.IssueClosed
			issue.ClosedAt = i.ClosedAt
			if issue.ClosedAt == nil {
				issue.ClosedAt = &issue.UpdatedAt
			}
		}
		added, err := im.c.Pers.AddIssue(issue, comments, im.labelIds(i.Labels), im.assignees(i.Assignees))
		if err != nil || !added {
			return err
		}
		im.imp.Issues++
		im.imp.Comments += len(comments)
		return im.save(map[string]any{"issues": im.imp.Issues, "comments": im.imp.Comments})
	})
}

func (im *importer) importPulls(ctx context.Context) error {
	if err := im.loadLabels(); err != nil {
		return err
	}
	if err := im.loadMilestones(); err != nil {
		return err
	}
	pulls := im.c.Pulls
	return im.src.Pulls(ctx, func(p forge.Pull) error {
		n, numberNote := im.number(im.imp.PullOffset + p.Number)
		if n > 0 {
			if _, err := pulls.Get(im.repo.Id, n); err == nil {
				return nil
			}
		}
		if im.imp.PullOffset > 0 && numberNote == "" {
			numberNote = fmt.Sprintf("*Originally !%d on %s*\n\n", p.Number, forge.Name(im.imp.Forge))
		}
		author, note := im.author(p.Author, p.CreatedAt)
		pr := &domain.PullRequest{RepositoryId: im.repo.Id, Number: n, Title: title(p.Title), Body: numberNote + note + p.Body, AuthorId: author,
			SourceBranch: p.SourceBranch, TargetBranch: p.TargetBranch, State: domain.PullRequestOpen, Draft: p.Draft && p.State == forge.PullOpen,
			MilestoneId: im.milestone(p.Milestone), CreatedAt: p.CreatedAt, UpdatedAt: later(p.UpdatedAt, p.CreatedAt)}
		if p.SourceOwner != "" {
			pr.SourceBranch = p.SourceOwner + ":" + p.SourceBranch // matches no branch here: read-only
		}
		head := im.commit(p.HeadSHA, p.HeadRef)
		base := im.commit(p.BaseSHA, "")
		if base == "" {
			base = im.commit("", "refs/heads/"+p.TargetBranch)
		}
		pr.HeadCommit, pr.BaseCommit = head, base
		switch p.State {
		case forge.PullMerged:
			pr.State, pr.MergedAt, pr.MergeCommit = domain.PullRequestMerged, p.MergedAt, im.commit(p.MergeCommit, "")
			pr.ClosedAt = p.MergedAt
			if p.MergedBy != nil {
				pr.MergedById = im.user(*p.MergedBy)
			}
		case forge.PullClosed:
			pr.State, pr.ClosedAt = domain.PullRequestClosed, p.ClosedAt
			if pr.ClosedAt == nil {
				pr.ClosedAt = &pr.UpdatedAt
			}
		}
		cs, err := im.src.PullComments(ctx, p)
		if err != nil {
			return err
		}
		var comments []domain.Comment
		for _, c := range cs {
			author, note := im.author(c.Author, c.CreatedAt)
			comments = append(comments, domain.Comment{AuthorId: author, Body: note + c.Body, CreatedAt: c.CreatedAt, UpdatedAt: later(c.UpdatedAt, c.CreatedAt)})
		}
		rs, err := im.src.Reviews(ctx, p)
		if err != nil {
			return err
		}
		var reviews []domain.ImportedReview
		for _, r := range rs {
			state, body := domain.ReviewState(r.State), r.Body
			reviewer := im.user(r.Author)
			if reviewer == nil {
				// The importer's name: never an approval of theirs.
				actor := im.imp.ActorId
				reviewer = &actor
				verdict := map[string]string{forge.Approved: "approved", forge.ChangesRequested: "requested changes"}[r.State]
				if verdict == "" {
					verdict = "reviewed"
				}
				body = fmt.Sprintf("*Originally %s by @%s on %s, %s*\n\n", verdict, r.Author.Login, forge.Name(im.imp.Forge), r.SubmittedAt.UTC().Format("2006-01-02")) + body
				state = domain.ReviewCommented
			}
			if strings.TrimSpace(body) != "" || state != domain.ReviewCommented {
				reviews = append(reviews, domain.ImportedReview{Review: domain.Review{AuthorId: reviewer, State: state, Body: body, CommitId: r.CommitId, CreatedAt: r.SubmittedAt}})
			}
			for _, c := range r.Comments { // inline: plain comments quoting the place
				author, note := im.author(c.Author, c.CreatedAt)
				place := "`" + c.Path + "`"
				if c.Line > 0 {
					place += fmt.Sprintf(" line %d", c.Line)
				}
				comments = append(comments, domain.Comment{AuthorId: author, Body: note + "On " + place + ":\n\n" + c.Body, CreatedAt: c.CreatedAt, UpdatedAt: later(c.UpdatedAt, c.CreatedAt)})
			}
		}
		added, err := im.c.Pers.AddPull(pr, comments, reviews, im.labelIds(p.Labels), im.assignees(p.Assignees))
		if err != nil || !added {
			return err
		}
		if head != "" { // keeps the head's commits (and finds it again)
			_ = im.st.SetReference(plumbing.NewHashReference(pullHeadRef(pr.Number), plumbing.NewHash(head)))
		}
		if p.HeadRef != "" && p.HeadRef != pullHeadRef(pr.Number).String() {
			_ = im.st.RemoveReference(plumbing.ReferenceName(p.HeadRef)) // GitLab's, renumbered
		}
		im.imp.Pulls++
		im.imp.Comments += len(comments)
		return im.save(map[string]any{"pulls": im.imp.Pulls, "comments": im.imp.Comments})
	})
}

// commit returns sha if the repository has it, else the commit ref points
// to, else "".
func (im *importer) commit(sha, ref string) string {
	if sha != "" {
		if h := plumbing.NewHash(sha); im.st.HasEncodedObject(h) == nil {
			return sha
		}
	}
	if ref != "" {
		if r, err := im.st.Reference(plumbing.ReferenceName(ref)); err == nil && im.st.HasEncodedObject(r.Hash()) == nil {
			return r.Hash().String()
		}
	}
	return ""
}

func (im *importer) maxAssetSize() int64 {
	if im.c.Config.Releases.MaxAssetSize > 0 {
		return im.c.Config.Releases.MaxAssetSize
	}
	return DefaultMaxAssetSize
}

func (im *importer) importReleases(ctx context.Context) error {
	rs, err := im.src.Releases(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if strings.TrimSpace(r.TagName) == "" {
			continue
		}
		rel, err := im.c.Releases.ByTag(im.repo.Id, r.TagName)
		if err != nil {
			author, note := im.author(r.Author, r.CreatedAt)
			body := note + r.Body
			if len(r.Links) > 0 {
				body += "\n\n**Assets**\n"
				for _, l := range r.Links {
					body += "\n- [" + l.Name + "](" + l.URL + ")"
				}
			}
			target := r.Target
			if target == "" {
				target = im.repo.DefaultBranch
			}
			draft := r.Draft
			if _, err := im.st.Reference(plumbing.NewTagReferenceName(r.TagName)); err != nil && !draft {
				draft = true // no such tag here: a draft until it's pushed
				im.warn("release %s: the tag isn't in the repository, imported as a draft", r.TagName)
			}
			rel = &domain.Release{RepositoryId: im.repo.Id, TagName: r.TagName, TargetCommitish: target, Name: r.Name, Body: body,
				Draft: draft, Prerelease: r.Prerelease, AuthorId: author, CreatedAt: r.CreatedAt, UpdatedAt: r.CreatedAt}
			if !draft {
				published := r.CreatedAt
				if r.PublishedAt != nil {
					published = *r.PublishedAt
				}
				rel.PublishedAt = &published
			}
			if err := im.c.Releases.Create(rel); err != nil {
				return err
			}
			im.imp.Releases++
			if err := im.save(map[string]any{"releases": im.imp.Releases}); err != nil {
				return err
			}
		}
		for _, a := range r.Assets {
			if err := im.importAsset(ctx, rel, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// importAsset copies an asset unless it's there or too large (a warning).
func (im *importer) importAsset(ctx context.Context, rel *domain.Release, a forge.Asset) error {
	name := domain.AssetName(a.Name)
	if name == "" {
		return nil
	}
	if _, err := im.c.Assets.ByName(rel.Id, name); err == nil {
		return nil
	}
	limit := im.maxAssetSize()
	if a.Size > limit {
		im.warn("release %s: %s is larger than releases.max_asset_size, skipped", rel.TagName, name)
		return nil
	}
	body, err := im.src.Download(ctx, a)
	var limited *forge.ErrRateLimited
	if errors.As(err, &limited) {
		return err
	} else if err != nil {
		im.warn("release %s: %s couldn't be downloaded (%v)", rel.TagName, name, err)
		return nil
	}
	defer body.Close()
	contentType := "application/octet-stream"
	if mt, params, err := mime.ParseMediaType(a.ContentType); err == nil {
		contentType = mime.FormatMediaType(mt, params)
	}
	row := &domain.ReleaseAsset{Id: utils.UUIDv4(), ReleaseId: rel.Id, RepositoryId: im.repo.Id, Name: name, Label: a.Label, ContentType: contentType}
	counted := &countingReader{r: io.LimitReader(body, limit+1)}
	if err := im.c.Storage.PutAsset(im.repo.StorageKey, row.Id, counted); err != nil {
		_ = im.c.Storage.DeleteAsset(im.repo.StorageKey, row.Id)
		return err
	}
	if counted.n > limit {
		_ = im.c.Storage.DeleteAsset(im.repo.StorageKey, row.Id)
		im.warn("release %s: %s is larger than releases.max_asset_size, skipped", rel.TagName, name)
		return nil
	}
	row.Size = counted.n
	if err := im.c.Assets.Create(row); err != nil {
		_ = im.c.Storage.DeleteAsset(im.repo.StorageKey, row.Id)
		return err
	}
	return nil
}

func later(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

func title(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return "(untitled)"
	}
	return t
}
