// Package browse serves repository content to the web UI. Its routes don't
// require a session: public repositories are readable by anyone, private
// ones by members (others get 404).
package browse

import (
	"errors"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/gofiber/fiber/v2"
	fiberoapi "github.com/labbs/fiber-oapi"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/interfaces/http/v1/apierror"
	"github.com/labbs/ogit/interfaces/http/v1/optauth"
	"github.com/labbs/ogit/interfaces/http/v1/repoid"
	"github.com/rs/zerolog"
)

const (
	maxFileBytes   = 1 << 20   // shown in the file view
	maxReadmeBytes = 512 << 10 // rendered under a directory
	maxBlameBytes  = 512 << 10
	maxLogPage     = 100
)

type Controller struct {
	Logger     zerolog.Logger
	SessionApp *application.SessionApp
	BrowseApp  *application.BrowseApp
}

// SpecRequest: spec is "<ref>/<path>" as in the page URL (the ref may
// contain slashes); empty means the default branch's root.
type SpecRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Spec       string `query:"spec" json:"-"`
}

type RefsResponse struct {
	DefaultBranch string          `json:"default_branch"`
	Branches      []gitbrowse.Ref `json:"branches"`
	Tags          []gitbrowse.Ref `json:"tags"`
}

type TreeResponse struct {
	Empty   bool              `json:"empty"` // no commits yet
	Ref     string            `json:"ref"`
	Path    string            `json:"path"`
	Commit  *gitbrowse.Commit `json:"commit,omitempty"`
	Entries []gitbrowse.Entry `json:"entries"`
	Readme  *gitbrowse.File   `json:"readme,omitempty"`
}

type BlobResponse struct {
	Ref    string           `json:"ref"`
	Commit gitbrowse.Commit `json:"commit"`
	File   *gitbrowse.File  `json:"file"`
}

type CommitsRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Spec       string `query:"spec" json:"-"` // <ref>[/<path>]
	Offset     int    `query:"offset" json:"-"`
	Limit      int    `query:"limit" json:"-"`
}

type CommitsResponse struct {
	Ref     string             `json:"ref"`
	Path    string             `json:"path"`
	Commits []gitbrowse.Commit `json:"commits"`
	More    bool               `json:"more"`
}

type CommitRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Sha        string `path:"sha" json:"-" validate:"required"`
}

type CommitResponse struct {
	Commit gitbrowse.Commit `json:"commit"`
	Diff   gitbrowse.Diff   `json:"diff"`
}

type CompareRequest struct {
	Identifier string `path:"identifier" json:"-" validate:"required"`
	Base       string `query:"base" json:"-" validate:"required"`
	Head       string `query:"head" json:"-" validate:"required"`
}

type BlameResponse struct {
	Ref   string                `json:"ref"`
	Path  string                `json:"path"`
	Lines []gitbrowse.BlameLine `json:"lines"`
}

func Setup(c Controller, repoGrp *fiberoapi.OApiGroup) {
	tags := []string{"Browse"}
	opts := func(summary, id, description string) fiberoapi.OpenAPIOptions {
		return fiberoapi.OpenAPIOptions{Summary: summary, OperationID: id, Tags: tags, Security: "disabled",
			Description: description + " No session needed for public repositories."}
	}
	fiberoapi.Get(repoGrp, "/:identifier/refs", c.Refs, opts("Branches and tags", "browse.refs", "Branches and tags, newest commit first, and the default branch."))
	fiberoapi.Get(repoGrp, "/:identifier/tree", c.Tree, opts("Directory", "browse.tree", "?spec=<ref>/<path> (the ref may contain slashes). Includes the directory's README, if any."))
	fiberoapi.Get(repoGrp, "/:identifier/blob", c.Blob, opts("File", "browse.blob", "?spec=<ref>/<path>. Text up to 1 MiB; binary files are flagged, not returned (use the raw URL)."))
	fiberoapi.Get(repoGrp, "/:identifier/commits", c.Commits, opts("History", "browse.commits", "?spec=<ref>[/<path>]&offset=&limit= (at most 100), newest first; with a path, only commits that change it."))
	fiberoapi.Get(repoGrp, "/:identifier/commits/:sha", c.Commit, opts("Commit", "browse.commit", "A commit and its diff against its first parent."))
	fiberoapi.Get(repoGrp, "/:identifier/compare", c.Compare, opts("Compare", "browse.compare", "?base=&head= (refs or commit hashes): the commits head adds to base and the diff from their merge base."))
	fiberoapi.Get(repoGrp, "/:identifier/blame", c.Blame, opts("Blame", "browse.blame", "?spec=<ref>/<path>: the commit that last changed each line (text files up to 512 KiB)."))
}

// fail maps browsing errors to statuses.
func (c Controller) fail(err error) *fiberoapi.ErrorResponse {
	switch {
	case errors.Is(err, gitbrowse.ErrRefNotFound):
		return &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: err.Error(), Type: "REF_NOT_FOUND"}
	case errors.Is(err, gitbrowse.ErrPathNotFound), errors.Is(err, gitbrowse.ErrNotAFile):
		return &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: err.Error(), Type: "PATH_NOT_FOUND"}
	case errors.Is(err, gitbrowse.ErrEmpty):
		return &fiberoapi.ErrorResponse{Code: fiber.StatusNotFound, Details: err.Error(), Type: "EMPTY_REPOSITORY"}
	case errors.Is(err, gitbrowse.ErrTooLarge):
		return &fiberoapi.ErrorResponse{Code: fiber.StatusUnprocessableEntity, Details: err.Error(), Type: "TOO_LARGE"}
	}
	return apierror.From(err, c.Logger)
}

// storer opens the repository if the caller (or anonymous) may read it.
func (c Controller) storer(ctx *fiber.Ctx, identifier string) (storer.Storer, *fiberoapi.ErrorResponse) {
	id, path := repoid.Split(identifier)
	_, st, err := c.BrowseApp.Open(id, path, optauth.UserID(ctx, c.SessionApp))
	if err != nil {
		return nil, c.fail(err)
	}
	return st, nil
}

func (c Controller) Refs(ctx *fiber.Ctx, req SpecRequest) (*RefsResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	def, branches, tags, err := gitbrowse.Refs(st)
	if err != nil {
		return nil, c.fail(err)
	}
	return &RefsResponse{DefaultBranch: def, Branches: nonNil(branches), Tags: nonNil(tags)}, nil
}

// resolve splits spec; an empty spec is the default branch's root.
func resolve(st storer.Storer, spec string) (string, string, *object.Commit, error) {
	if spec == "" {
		c, err := gitbrowse.ResolveRef(st, "")
		if err != nil {
			return "", "", nil, err
		}
		def, _, _, _ := gitbrowse.Refs(st)
		return def, "", c, nil
	}
	return gitbrowse.SplitRefPath(st, spec)
}

func (c Controller) Tree(ctx *fiber.Ctx, req SpecRequest) (*TreeResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	ref, path, commit, err := resolve(st, req.Spec)
	if errors.Is(err, gitbrowse.ErrEmpty) {
		return &TreeResponse{Empty: true, Entries: []gitbrowse.Entry{}}, nil
	} else if err != nil {
		return nil, c.fail(err)
	}
	entries, err := gitbrowse.Tree(commit, path)
	if err != nil {
		return nil, c.fail(err)
	}
	info := gitbrowse.CommitInfo(commit)
	resp := &TreeResponse{Ref: ref, Path: path, Commit: &info, Entries: entries}
	if readme := gitbrowse.Readme(entries); readme != nil {
		if f, err := gitbrowse.ReadFile(commit, readme.Path, maxReadmeBytes); err == nil && !f.Binary {
			resp.Readme = f
		}
	}
	return resp, nil
}

func (c Controller) Blob(ctx *fiber.Ctx, req SpecRequest) (*BlobResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	ref, path, commit, err := resolve(st, req.Spec)
	if err != nil {
		return nil, c.fail(err)
	}
	f, err := gitbrowse.ReadFile(commit, path, maxFileBytes)
	if err != nil {
		return nil, c.fail(err)
	}
	return &BlobResponse{Ref: ref, Commit: gitbrowse.CommitInfo(commit), File: f}, nil
}

func (c Controller) Commits(ctx *fiber.Ctx, req CommitsRequest) (*CommitsResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	ref, path, commit, err := resolve(st, req.Spec)
	if errors.Is(err, gitbrowse.ErrEmpty) {
		return &CommitsResponse{Commits: []gitbrowse.Commit{}}, nil
	} else if err != nil {
		return nil, c.fail(err)
	}
	limit := req.Limit
	if limit <= 0 || limit > maxLogPage {
		limit = 30
	}
	commits, more, err := gitbrowse.Log(commit, path, max(req.Offset, 0), limit)
	if err != nil {
		return nil, c.fail(err)
	}
	return &CommitsResponse{Ref: ref, Path: path, Commits: nonNil(commits), More: more}, nil
}

func (c Controller) Commit(ctx *fiber.Ctx, req CommitRequest) (*CommitResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	commit, err := gitbrowse.GetCommit(st, req.Sha)
	if err != nil {
		return nil, c.fail(err)
	}
	var parent *object.Commit
	if commit.NumParents() > 0 {
		if parent, err = commit.Parent(0); err != nil {
			return nil, c.fail(err)
		}
	}
	diff, err := gitbrowse.DiffCommits(parent, commit, gitbrowse.DefaultLimits)
	if err != nil {
		return nil, c.fail(err)
	}
	return &CommitResponse{Commit: gitbrowse.CommitInfo(commit), Diff: *diff}, nil
}

func (c Controller) Compare(ctx *fiber.Ctx, req CompareRequest) (*gitbrowse.Comparison, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	base, err := gitbrowse.ResolveRef(st, req.Base)
	if err != nil {
		return nil, c.fail(err)
	}
	head, err := gitbrowse.ResolveRef(st, req.Head)
	if err != nil {
		return nil, c.fail(err)
	}
	cmp, err := gitbrowse.Compare(base, head, gitbrowse.DefaultLimits)
	if err != nil {
		return nil, c.fail(err)
	}
	return cmp, nil
}

func (c Controller) Blame(ctx *fiber.Ctx, req SpecRequest) (*BlameResponse, *fiberoapi.ErrorResponse) {
	st, errResp := c.storer(ctx, req.Identifier)
	if errResp != nil {
		return nil, errResp
	}
	ref, path, commit, err := resolve(st, req.Spec)
	if err != nil {
		return nil, c.fail(err)
	}
	lines, err := gitbrowse.Blame(commit, path, maxBlameBytes)
	if err != nil {
		return nil, c.fail(err)
	}
	return &BlameResponse{Ref: ref, Path: path, Lines: lines}, nil
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
