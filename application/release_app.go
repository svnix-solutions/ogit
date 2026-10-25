package application

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Release rules:
//   - one release per tag; anyone who can read the repository sees its
//     published releases; drafts are visible to writers only;
//   - writers (developers and up, or a repository token with
//     write:repository) create, edit, publish and delete releases;
//   - a release names an existing tag, or a new one created from
//     target_commitish (a branch or commit; the default branch when empty)
//     when the release is published. Deleting a release keeps its tag;
//   - pre-releases are published but never "latest";
//   - notes can be generated from the pull requests merged since the
//     previous release;
//   - assets: see release_asset.go;
//   - "release" webhook events (published, released, prereleased, created,
//     edited, deleted) are sent for published releases only: drafts stay
//     between writers.

var (
	ErrReleaseNotFound = errors.New("release not found")
	ErrReleaseExists   = errors.New("this tag already has a release")
	ErrInvalidRelease  = errors.New("a release needs a valid tag name; its target must be a branch or commit of the repository")
	ErrTagNotFound     = errors.New("no such tag")
)

type ReleaseApp struct {
	Logger       zerolog.Logger
	Repositories *RepositoryApp
	Releases     domain.ReleasePers
	Assets       domain.ReleaseAssetPers // release_asset.go
	MaxAssetSize int64                   // bytes; 0: 100 MB
	Pulls        domain.PullRequestPers
	Users        domain.UserPers
	Webhooks     *WebhookApp
	// TagCheck decides whether a release may create a tag (protected tags);
	// nil: no rules.
	TagCheck func(repo *domain.Repository, caller StatusCaller, tag string) error
}

// ReleaseView is a release with what the caller may do.
type ReleaseView struct {
	*domain.Release
	Repository *domain.Repository
	CanEdit    bool
	Assets     []domain.ReleaseAsset
}

func (c *ReleaseApp) open(id, path *string, caller StatusCaller) (*domain.Repository, storer.Storer, bool, error) {
	if !caller.tokenAllows(domain.ScopeReadRepository, domain.ScopeWriteRepository) {
		return nil, nil, false, ErrTokenScope
	}
	repo, err := c.Repositories.GetRepositoryForUser(id, path, caller.UserId)
	if err != nil {
		return nil, nil, false, err
	}
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, nil, false, err
	}
	return repo, st, c.canWrite(repo, caller), nil
}

// canWrite: a person with write access, or this repository's token with
// write:repository.
func (c *ReleaseApp) canWrite(repo *domain.Repository, caller StatusCaller) bool {
	if repo.IsArchived {
		return false
	}
	if domain.IsPerson(caller.UserId) {
		role, err := c.Repositories.GetUserEffectiveRole(repo.Id, caller.UserId)
		return err == nil && role.CanWrite() && caller.tokenAllows(domain.ScopeWriteRepository)
	}
	return caller.Token != nil && caller.Token.RepositoryId != nil && *caller.Token.RepositoryId == repo.Id &&
		caller.tokenAllows(domain.ScopeWriteRepository)
}

// List returns a page of releases and how many the caller can see.
func (c *ReleaseApp) List(id, path *string, caller StatusCaller, offset, limit int) ([]ReleaseView, int64, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, 0, err
	}
	releases, err := c.Releases.List(repo.Id, writer, max(offset, 0), clampLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	total, err := c.Releases.Count(repo.Id, writer)
	if err != nil {
		return nil, 0, err
	}
	out := make([]ReleaseView, 0, len(releases))
	for i := range releases {
		out = append(out, ReleaseView{Release: &releases[i], Repository: repo, CanEdit: writer})
	}
	return out, total, c.withAssets(out)
}

func (c *ReleaseApp) view(repo *domain.Repository, writer bool, r *domain.Release, err error) (*ReleaseView, error) {
	if isNotFound(err) || (err == nil && r.Draft && !writer) {
		return nil, ErrReleaseNotFound
	} else if err != nil {
		return nil, err
	}
	views := []ReleaseView{{Release: r, Repository: repo, CanEdit: writer}}
	if err := c.withAssets(views); err != nil {
		return nil, err
	}
	return &views[0], nil
}

func (c *ReleaseApp) Get(id, path *string, caller StatusCaller, releaseId string) (*ReleaseView, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	r, err := c.Releases.Get(repo.Id, releaseId)
	return c.view(repo, writer, r, err)
}

func (c *ReleaseApp) ByTag(id, path *string, caller StatusCaller, tag string) (*ReleaseView, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	r, err := c.Releases.ByTag(repo.Id, tag)
	return c.view(repo, writer, r, err)
}

// ByApiId: Get by the GitHub-compatible numeric id.
func (c *ReleaseApp) ByApiId(id, path *string, caller StatusCaller, apiId int64) (*ReleaseView, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	r, err := c.Releases.ByApiId(repo.Id, apiId)
	return c.view(repo, writer, r, err)
}

func (c *ReleaseApp) Latest(id, path *string, caller StatusCaller) (*ReleaseView, error) {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	r, err := c.Releases.Latest(repo.Id)
	return c.view(repo, writer, r, err)
}

// ReleaseInput holds a release's fields; nil ones are kept on update.
type ReleaseInput struct {
	TagName         *string
	TargetCommitish *string
	Name            *string
	Body            *string
	Draft           *bool
	Prerelease      *bool
}

func (c *ReleaseApp) Create(id, path *string, caller StatusCaller, in ReleaseInput) (*ReleaseView, error) {
	repo, st, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	if !writer {
		return nil, ErrForbidden
	}
	r := &domain.Release{RepositoryId: repo.Id}
	if domain.IsPerson(caller.UserId) {
		r.AuthorId = &caller.UserId
	}
	apply(r, in)
	if r.TargetCommitish == "" {
		r.TargetCommitish = repo.DefaultBranch
	}
	if !domain.ValidTagName(r.TagName) {
		return nil, ErrInvalidRelease
	}
	if _, err := c.Releases.ByTag(repo.Id, r.TagName); err == nil {
		return nil, ErrReleaseExists
	}
	if !r.Draft {
		if err := c.ensureTag(repo, st, caller, r); err != nil {
			return nil, err
		}
		now := time.Now()
		r.PublishedAt = &now
	} else if _, err := c.tagTarget(st, r); err != nil {
		return nil, err // a draft's target must exist already
	}
	if err := c.Releases.Create(r); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrReleaseExists
	} else if err != nil {
		return nil, err
	}
	v, err := c.Get(&repo.Id, nil, caller, r.Id)
	if err != nil {
		return nil, err
	}
	if !r.Draft {
		c.published(v, caller)
	}
	return v, nil
}

func apply(r *domain.Release, in ReleaseInput) {
	if in.TagName != nil {
		r.TagName = strings.TrimSpace(*in.TagName)
	}
	if in.TargetCommitish != nil {
		r.TargetCommitish = strings.TrimSpace(*in.TargetCommitish)
	}
	if in.Name != nil {
		r.Name = strings.TrimSpace(*in.Name)
	}
	if in.Body != nil {
		r.Body = *in.Body
	}
	if in.Draft != nil {
		r.Draft = *in.Draft
	}
	if in.Prerelease != nil {
		r.Prerelease = *in.Prerelease
	}
}

// tagTarget returns the commit a release's tag points (or will point) to.
func (c *ReleaseApp) tagTarget(st storer.Storer, r *domain.Release) (*object.Commit, error) {
	if ref, err := storer.ResolveReference(st, plumbing.NewTagReferenceName(r.TagName)); err == nil {
		return peelToCommit(st, ref.Hash())
	}
	commit, err := gitbrowse.ResolveRef(st, r.TargetCommitish)
	if err != nil {
		return nil, ErrInvalidRelease
	}
	return commit, nil
}

func peelToCommit(st storer.Storer, h plumbing.Hash) (*object.Commit, error) {
	if tag, err := object.GetTag(st, h); err == nil {
		return tag.Commit()
	}
	return object.GetCommit(st, h)
}

// ensureTag creates the release's tag from its target if it doesn't exist.
func (c *ReleaseApp) ensureTag(repo *domain.Repository, st storer.Storer, caller StatusCaller, r *domain.Release) error {
	name := plumbing.NewTagReferenceName(r.TagName)
	if _, err := st.Reference(name); err == nil {
		return nil
	}
	if c.TagCheck != nil {
		if err := c.TagCheck(repo, caller, r.TagName); err != nil {
			return err
		}
	}
	commit, err := c.tagTarget(st, r)
	if err != nil {
		return err
	}
	ref := plumbing.NewHashReference(name, commit.Hash)
	if creator, ok := st.(helpergit.ReferenceCreator); ok {
		err = creator.CreateReference(ref)
	} else {
		err = st.SetReference(ref)
	}
	if err != nil {
		return err
	}
	pusher := Pusher{UserId: caller.UserId}
	if !domain.IsPerson(caller.UserId) {
		pusher = Pusher{Name: "token " + caller.Token.Name}
	}
	c.Webhooks.Pushed(repo, pusher, st, []RefUpdate{{Name: name, New: commit.Hash}})
	return nil
}

func (c *ReleaseApp) Update(id, path *string, caller StatusCaller, releaseId string, in ReleaseInput) (*ReleaseView, error) {
	repo, st, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	current, err := c.Releases.Get(repo.Id, releaseId)
	if _, err := c.view(repo, writer, current, err); err != nil {
		return nil, err
	}
	if !writer {
		return nil, ErrForbidden
	}
	before := *current
	r := current
	apply(r, in)
	if !domain.ValidTagName(r.TagName) {
		return nil, ErrInvalidRelease
	}
	if r.TagName != before.TagName {
		if !before.Draft {
			return nil, ErrInvalidRelease // a published release keeps its tag
		}
		if other, err := c.Releases.ByTag(repo.Id, r.TagName); err == nil && other.Id != r.Id {
			return nil, ErrReleaseExists
		}
	}
	publishing := before.Draft && !r.Draft
	if r.Draft && !before.Draft {
		return nil, ErrInvalidRelease // published releases don't go back to draft
	}
	fields := map[string]any{"tag_name": r.TagName, "target_commitish": r.TargetCommitish, "name": r.Name, "body": r.Body,
		"draft": r.Draft, "prerelease": r.Prerelease, "updated_at": time.Now()}
	if publishing {
		if err := c.ensureTag(repo, st, caller, r); err != nil {
			return nil, err
		}
		fields["published_at"] = time.Now()
	} else if r.Draft {
		if _, err := c.tagTarget(st, r); err != nil {
			return nil, err
		}
	}
	if err := c.Releases.Update(r.Id, fields); helperError.Catch(err) == gorm.ErrDuplicatedKey {
		return nil, ErrReleaseExists
	} else if err != nil {
		return nil, err
	}
	v, err := c.Get(&repo.Id, nil, caller, r.Id)
	if err != nil {
		return nil, err
	}
	switch {
	case publishing:
		c.published(v, caller)
	case !r.Draft:
		changes := map[string]any{}
		if before.Name != r.Name {
			changes["name"] = map[string]string{"from": before.Name}
		}
		if before.Body != r.Body {
			changes["body"] = map[string]string{"from": before.Body}
		}
		c.Webhooks.ReleaseEvent(v, "edited", c.sender(caller), changes)
		if before.Prerelease && !r.Prerelease {
			c.Webhooks.ReleaseEvent(v, "released", c.sender(caller), nil)
		}
	}
	return v, nil
}

func (c *ReleaseApp) sender(caller StatusCaller) string {
	if domain.IsPerson(caller.UserId) {
		return caller.UserId
	}
	return ""
}

// published sends the events of a release that was just published.
func (c *ReleaseApp) published(v *ReleaseView, caller StatusCaller) {
	sender := c.sender(caller)
	c.Webhooks.ReleaseEvent(v, "created", sender, nil)
	c.Webhooks.ReleaseEvent(v, "published", sender, nil)
	if v.Prerelease {
		c.Webhooks.ReleaseEvent(v, "prereleased", sender, nil)
	} else {
		c.Webhooks.ReleaseEvent(v, "released", sender, nil)
	}
}

func (c *ReleaseApp) Delete(id, path *string, caller StatusCaller, releaseId string) error {
	repo, _, writer, err := c.open(id, path, caller)
	if err != nil {
		return err
	}
	r, err := c.Releases.Get(repo.Id, releaseId)
	v, err := c.view(repo, writer, r, err)
	if err != nil {
		return err
	}
	if !writer {
		return ErrForbidden
	}
	if err := c.Releases.Delete(r.Id); err != nil {
		return err
	}
	c.deleteFiles(repo, v.Assets)
	if !r.Draft {
		c.Webhooks.ReleaseEvent(v, "deleted", c.sender(caller), nil)
	}
	return nil
}

// GeneratedNotes is a suggested release name and body.
type GeneratedNotes struct {
	Name string
	Body string
}

// maxNotesCommits bounds the walk from the new tag to the previous one.
const maxNotesCommits = 5000

// GenerateNotes lists the pull requests merged between the previous
// release (previousTag, or the latest published release) and tag (which
// may not exist yet: then target).
func (c *ReleaseApp) GenerateNotes(id, path *string, caller StatusCaller, tag, target, previousTag string) (*GeneratedNotes, error) {
	repo, st, writer, err := c.open(id, path, caller)
	if err != nil {
		return nil, err
	}
	if !writer {
		return nil, ErrForbidden
	}
	if target == "" {
		target = repo.DefaultBranch
	}
	head, err := c.tagTarget(st, &domain.Release{TagName: tag, TargetCommitish: target})
	if err != nil {
		return nil, err
	}
	if previousTag == "" {
		if prev, err := c.Releases.Previous(repo.Id, time.Now()); err == nil && prev.TagName != tag {
			previousTag = prev.TagName
		}
	}
	var stop plumbing.Hash
	if previousTag != "" {
		// The tag itself: an empty target would mean HEAD.
		ref, err := storer.ResolveReference(st, plumbing.NewTagReferenceName(previousTag))
		if err != nil {
			return nil, ErrTagNotFound
		}
		prev, err := peelToCommit(st, ref.Hash())
		if err != nil {
			return nil, ErrTagNotFound
		}
		stop = prev.Hash
	}
	// The commits since the previous release: reachable from head, not
	// from stop (both walks bounded).
	previous := map[plumbing.Hash]bool{}
	if !stop.IsZero() {
		if c, err := object.GetCommit(st, stop); err == nil {
			previous = walk(c, nil, 4*maxNotesCommits)
		}
	}
	since := walk(head, previous, maxNotesCommits)
	merged, _, err := c.Pulls.List(repo.Id, domain.PullRequestMerged, "", 0, 500)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("## What's Changed\n\n")
	n := 0
	for i := len(merged) - 1; i >= 0; i-- { // oldest first
		pr := merged[i]
		if pr.MergeCommit == "" || !since[plumbing.NewHash(pr.MergeCommit)] {
			continue
		}
		author := "ghost"
		if pr.Author != nil {
			author = pr.Author.Username
		}
		fmt.Fprintf(&b, "* %s by @%s in #%d\n", pr.Title, author, pr.Number)
		n++
	}
	if n == 0 {
		b.WriteString("No pull requests were merged.\n")
	}
	if previousTag != "" {
		fmt.Fprintf(&b, "\n**Full Changelog**: %s...%s\n", previousTag, tag)
	}
	return &GeneratedNotes{Name: tag, Body: b.String()}, nil
}

// walk returns the commits reachable from tip (all parents), not crossing
// the ones in stop, at most limit.
func walk(tip *object.Commit, stop map[plumbing.Hash]bool, limit int) map[plumbing.Hash]bool {
	seen := map[plumbing.Hash]bool{}
	queue := []*object.Commit{tip}
	for len(queue) > 0 && len(seen) < limit {
		cm := queue[0]
		queue = queue[1:]
		if seen[cm.Hash] || stop[cm.Hash] {
			continue
		}
		seen[cm.Hash] = true
		_ = cm.Parents().ForEach(func(p *object.Commit) error {
			queue = append(queue, p)
			return nil
		})
	}
	return seen
}
