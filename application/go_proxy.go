package application

import (
	"errors"
	"github.com/labbs/ogit/domain"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Go module proxy rules (GOPROXY=<site>/api/packages/go):
//   - a repository is the module <host>/<namespace>/<slug> (the namespace
//     may be a sub-group's path), and
//     <host>/<namespace>/<slug>/vN for major versions N >= 2; <host> is
//     http.public_url's host name (module paths can't have ports), else the
//     request's;
//   - versions are the repository's canonical semver tags (vX.Y.Z[-pre])
//     of the module's major version whose go.mod declares the module path;
//     v0 and v1 tags without a go.mod count too (the go.mod is
//     synthesized), like on proxy.golang.org; nothing is published;
//   - branches and commits resolve to pseudo-versions based on the highest
//     tag among their ancestors; pseudo-versions resolve back by commit
//     hash and time;
//   - access is git's (the raw routes' auth): public repositories for
//     anyone, private ones with a token that reads the repository;
//   - ?go-get=1 on <site>/<namespace>/<slug>[/...] answers a go-import
//     "mod" meta tag pointing at the proxy, so GOPRIVATE=<host> works
//     without GOPROXY.

var (
	ErrGoModuleNotFound  = errors.New("module not found")
	ErrGoVersionNotFound = errors.New("unknown revision")
)

// historyLimit bounds the commits walked to resolve pseudo-versions.
const historyLimit = 10000

type GoProxyApp struct {
	Config config.Config
}

// Host is the host name module paths start with.
func (c *GoProxyApp) Host(requestHost string) string {
	if u, err := url.Parse(c.Config.Server.PublicURL); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	if h, _, ok := strings.Cut(requestHost, ":"); ok {
		return h
	}
	return requestHost
}

// GoModule is a module path resolved to its repository.
type GoModule struct {
	Path      string // the module path
	Repo      string // <namespace>/<slug>
	PathMajor string // "" or "/vN"
}

// SplitModule maps a module path of this host to its repository.
func (c *GoProxyApp) SplitModule(host, path string) (*GoModule, error) {
	rest, ok := strings.CutPrefix(path, host+"/")
	if !ok || module.CheckPath(path) != nil {
		return nil, ErrGoModuleNotFound
	}
	prefix, pathMajor, ok := module.SplitPathVersion(rest)
	if !ok || strings.HasPrefix(pathMajor, ".") { // gopkg.in style
		return nil, ErrGoModuleNotFound
	}
	if n := strings.Count(prefix, "/"); n < 1 || n > domain.MaxGroupDepth {
		return nil, ErrGoModuleNotFound
	}
	return &GoModule{Path: path, Repo: prefix, PathMajor: pathMajor}, nil
}

// major is the semver major of the module's versions ("v0" covers v0 and
// v1).
func (m *GoModule) major() string {
	if m.PathMajor == "" {
		return "v0"
	}
	return strings.TrimPrefix(m.PathMajor, "/")
}

func (m *GoModule) accepts(version string) bool {
	if m.PathMajor == "" {
		maj := semver.Major(version)
		return maj == "v0" || maj == "v1"
	}
	return semver.Major(version) == m.major()
}

// GoVersion is a version and its commit.
type GoVersion struct {
	Version string
	Time    time.Time
	Commit  *object.Commit
}

// fits reports whether a commit holds the module: its go.mod declares the
// path, or (v0/v1) it has none.
func (m *GoModule) fits(c *object.Commit) bool {
	gomod, err := gitbrowse.GoMod(c)
	if err != nil {
		return false
	}
	if gomod == nil {
		return m.PathMajor == ""
	}
	return gitbrowse.ModulePath(gomod) == m.Path
}

// GoMod is the module's go.mod at a commit (synthesized when missing).
func (m *GoModule) GoMod(c *object.Commit) ([]byte, error) {
	gomod, err := gitbrowse.GoMod(c)
	if err != nil {
		return nil, err
	}
	if gomod == nil {
		return []byte("module " + m.Path + "\n"), nil
	}
	return gomod, nil
}

type tagCommit struct {
	version string
	hash    plumbing.Hash
}

// tags lists the module's semver tags, highest first, without checking
// their go.mod.
func (m *GoModule) tags(st storer.Storer) ([]tagCommit, error) {
	iter, err := st.IterReferences()
	if err != nil {
		return nil, err
	}
	var out []tagCommit
	err = iter.ForEach(func(r *plumbing.Reference) error {
		if !r.Name().IsTag() || r.Type() != plumbing.HashReference {
			return nil
		}
		v := r.Name().Short()
		if semver.IsValid(v) && semver.Canonical(v) == v && m.accepts(v) {
			out = append(out, tagCommit{v, r.Hash()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return semver.Compare(out[i].version, out[j].version) > 0 })
	return out, err
}

// Versions lists the module's tagged versions, highest first.
func (m *GoModule) Versions(st storer.Storer) ([]GoVersion, error) {
	tags, err := m.tags(st)
	if err != nil {
		return nil, err
	}
	var out []GoVersion
	for _, t := range tags {
		c, err := gitbrowse.ResolveRef(st, t.version)
		if err != nil || !m.fits(c) {
			continue
		}
		out = append(out, GoVersion{Version: t.version, Time: c.Committer.When.UTC(), Commit: c})
	}
	return out, nil
}

// Latest is the highest release, else the highest pre-release, else a
// pseudo-version of the default branch.
func (m *GoModule) Latest(st storer.Storer) (*GoVersion, error) {
	versions, err := m.Versions(st)
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		if semver.Prerelease(v.Version) == "" {
			return &v, nil
		}
	}
	if len(versions) > 0 {
		return &versions[0], nil
	}
	return m.Query(st, "HEAD")
}

// Version resolves a version (a tag or a pseudo-version).
func (m *GoModule) Version(st storer.Storer, version string) (*GoVersion, error) {
	if !semver.IsValid(version) || !m.accepts(version) {
		return nil, ErrGoVersionNotFound
	}
	if module.IsPseudoVersion(version) {
		return m.pseudo(st, version)
	}
	c, err := gitbrowse.ResolveRef(st, version)
	if err != nil || !m.fits(c) {
		return nil, ErrGoVersionNotFound
	}
	return &GoVersion{Version: version, Time: c.Committer.When.UTC(), Commit: c}, nil
}

// Query resolves what `go get module@query` asks for: a version, a
// branch, a tag or a commit hash (pseudo-versions for the last ones).
func (m *GoModule) Query(st storer.Storer, query string) (*GoVersion, error) {
	if semver.IsValid(query) {
		if v, err := m.Version(st, query); err == nil {
			return v, nil
		}
	}
	c, err := gitbrowse.ResolveRef(st, query)
	if err != nil && len(query) >= 7 && len(query) < 40 && isHex(query) {
		c, err = findCommit(st, query)
	}
	if err != nil || !m.fits(c) {
		return nil, ErrGoVersionNotFound
	}
	// A tag of the module at that commit wins.
	if tags, err := m.tags(st); err == nil {
		for _, t := range tags {
			if tc, err := gitbrowse.ResolveRef(st, t.version); err == nil && tc.Hash == c.Hash {
				return &GoVersion{Version: t.version, Time: c.Committer.When.UTC(), Commit: c}, nil
			}
		}
	}
	older, err := m.olderTag(st, c)
	if err != nil {
		return nil, err
	}
	major := m.major()
	if older != "" {
		major = semver.Major(older)
	}
	t := c.Committer.When.UTC()
	return &GoVersion{Version: module.PseudoVersion(major, older, t, c.Hash.String()[:12]), Time: t, Commit: c}, nil
}

// olderTag is the highest tag of the module among c's ancestors.
func (m *GoModule) olderTag(st storer.Storer, c *object.Commit) (string, error) {
	tags, err := m.tags(st)
	if err != nil || len(tags) == 0 {
		return "", err
	}
	byHash := map[plumbing.Hash][]string{}
	for _, t := range tags {
		if tc, err := gitbrowse.ResolveRef(st, t.version); err == nil {
			byHash[tc.Hash] = append(byHash[tc.Hash], t.version)
		}
	}
	best := ""
	err = walkHistory(st, []plumbing.Hash{c.Hash}, func(h plumbing.Hash) bool {
		for _, v := range byHash[h] {
			if best == "" || semver.Compare(v, best) > 0 {
				best = v
			}
		}
		return false
	})
	return best, err
}

// pseudo resolves a pseudo-version: its commit (by hash prefix, among the
// branches' and tags' history) must have its time.
func (m *GoModule) pseudo(st storer.Storer, version string) (*GoVersion, error) {
	rev, err := module.PseudoVersionRev(version)
	if err != nil {
		return nil, ErrGoVersionNotFound
	}
	when, err := module.PseudoVersionTime(version)
	if err != nil {
		return nil, ErrGoVersionNotFound
	}
	c, err := findCommit(st, rev)
	if err != nil || !c.Committer.When.UTC().Truncate(time.Second).Equal(when) || !m.fits(c) {
		return nil, ErrGoVersionNotFound
	}
	return &GoVersion{Version: version, Time: when, Commit: c}, nil
}

// findCommit finds a commit by hash prefix in the history of every branch
// and tag.
func findCommit(st storer.Storer, prefix string) (*object.Commit, error) {
	prefix = strings.ToLower(prefix)
	if len(prefix) == 40 {
		return gitbrowse.GetCommit(st, prefix)
	}
	iter, err := st.IterReferences()
	if err != nil {
		return nil, err
	}
	var starts []plumbing.Hash
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference && (r.Name().IsBranch() || r.Name().IsTag()) {
			if c, err := gitbrowse.ResolveRef(st, r.Name().Short()); err == nil {
				starts = append(starts, c.Hash)
			}
		}
		return nil
	})
	var found plumbing.Hash
	err = walkHistory(st, starts, func(h plumbing.Hash) bool {
		if strings.HasPrefix(h.String(), prefix) {
			found = h
			return true
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	if found.IsZero() {
		return nil, ErrGoVersionNotFound
	}
	return object.GetCommit(st, found)
}

// walkHistory visits commits reachable from starts (at most historyLimit)
// until visit returns true.
func walkHistory(st storer.Storer, starts []plumbing.Hash, visit func(plumbing.Hash) bool) error {
	seen := map[plumbing.Hash]bool{}
	queue := append([]plumbing.Hash{}, starts...)
	for len(queue) > 0 && len(seen) < historyLimit {
		h := queue[0]
		queue = queue[1:]
		if seen[h] {
			continue
		}
		seen[h] = true
		if visit(h) {
			return nil
		}
		c, err := object.GetCommit(st, h)
		if err != nil {
			continue // shallow or missing: stop there
		}
		queue = append(queue, c.ParentHashes...)
	}
	return nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// GoModuleView describes a repository's Go module for the web UI.
type GoModuleView struct {
	Path     string
	Versions []GoVersion
	Requires []gitbrowse.Requirement // of the latest version (else HEAD)
}

// RepoModule is the module a repository serves (from its default branch's
// go.mod, whose path must be this host's for the repository), or
// ErrGoModuleNotFound. A summary skips the versions (every tag's go.mod)
// and requirements.
func (c *GoProxyApp) RepoModule(st storer.Storer, repoPath, requestHost string, summary bool) (*GoModuleView, error) {
	head, err := gitbrowse.ResolveRef(st, "HEAD")
	if err != nil {
		return nil, ErrGoModuleNotFound
	}
	gomod, err := gitbrowse.GoMod(head)
	if err != nil || gomod == nil {
		return nil, ErrGoModuleNotFound
	}
	m, err := c.SplitModule(c.Host(requestHost), gitbrowse.ModulePath(gomod))
	if err != nil || m.Repo != repoPath {
		return nil, ErrGoModuleNotFound
	}
	if summary {
		return &GoModuleView{Path: m.Path}, nil
	}
	versions, err := m.Versions(st)
	if err != nil {
		return nil, err
	}
	view := &GoModuleView{Path: m.Path, Versions: versions, Requires: gitbrowse.Requirements(gomod)}
	if latest, err := m.Latest(st); err == nil && latest.Commit != nil {
		if gm, err := m.GoMod(latest.Commit); err == nil {
			view.Requires = gitbrowse.Requirements(gm)
		}
	}
	if view.Requires == nil {
		view.Requires = []gitbrowse.Requirement{}
	}
	return view, nil
}
