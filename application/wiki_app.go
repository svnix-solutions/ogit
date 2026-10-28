package application

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/infrastructure/gitmerge"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

// Wiki rules:
//   - a repository's wiki is a second git repository (storage key
//     <key>.wiki), cloned and pushed at <path>.wiki.git over HTTP and SSH
//     with the repository's credentials; the first edit or push creates
//     it; it is deleted, renamed, transferred and forked with its
//     repository;
//   - whoever reads the repository reads its wiki; developers and up edit
//     it, or every signed-in reader when wiki_access is readers;
//     maintainers set wiki_access (writers, readers, off). Off hides it
//     (404 everywhere, git included) and keeps its content;
//   - archived repositories' and mirrors' wikis are read-only (mirrors
//     fetch theirs);
//   - pages are Markdown files (.md, or .markdown when read) on the wiki's
//     default branch, at the root or in folders; a page's name is its path
//     without the extension, its title the last segment with dashes as
//     spaces. Home is the front page; _Sidebar and _Footer (the nearest
//     one, from the page's folder up) are shown with pages. uploads/ holds
//     attachments, never pages;
//   - an edit names the commit it started from (base): when the page
//     changed since, it is a conflict; edits of other pages meanwhile are
//     not (the edit applies on top). Renames and deletions follow the same
//     rule; creating a page that exists is a conflict;
//   - revert writes a page as it was at a commit (a new commit; deleting
//     it if it didn't exist then);
//   - search looks for every word (case-insensitive) in titles and
//     content, bounded (wikiSearchPages, wikiSearchBytes);
//   - attachments are stored as Git LFS objects in the wiki's LFS store
//     and committed as pointers under uploads/<random>/<name>; they are
//     served by the raw route (<path>.wiki/raw/...) like any raw file,
//     never as HTML;
//   - every change (edit or push to the default branch) sends a gollum
//     webhook event and notifies the repository's watchers at "all".

var (
	ErrWikiNotFound     = errors.New("wiki not found")
	ErrWikiPageNotFound = errors.New("wiki page not found")
	ErrWikiPageExists   = errors.New("a wiki page with this name already exists")
	ErrWikiConflict     = errors.New("the page changed since you started editing it: reload it and apply your changes again")
	ErrWikiInvalidName  = errors.New("page names are 1-200 characters, folders separated by /, without \\ : * ? \" < > | # %, control characters or segments starting with a dot; uploads/ is reserved")
	ErrWikiTooLarge     = errors.New("pages are at most 1 MiB")
	ErrWikiInvalidFile  = errors.New("attachments need a file name and at most the size limit")
	ErrInvalidWikiMode  = errors.New("wiki_access must be writers, readers or off")
)

const (
	wikiMaxPageBytes   = 1 << 20
	wikiMaxPages       = 10000
	wikiSearchPages    = 2000
	wikiSearchBytes    = 32 << 20
	wikiSearchResults  = 50
	wikiMaxName        = 200
	wikiMaxDepth       = 6
	wikiUploads        = "uploads"
	wikiCASRetries     = 5
	wikiDefaultMaxFile = 25 << 20
)

type WikiApp struct {
	Logger        zerolog.Logger
	Repositories  *RepositoryApp
	Storage       storage.Config
	Users         domain.UserPers
	Webhooks      *WebhookApp
	Notifications *NotificationApp
	// MaxAttachment is the attachment size limit (0: 25 MiB).
	MaxAttachment int64
	now           func() time.Time
}

func (c *WikiApp) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// wikiFor is repo's wiki; create makes it when it doesn't exist yet
// (otherwise a missing wiki isn't found).
func (c *RepositoryApp) wikiFor(repo *domain.Repository, create bool) (*domain.Repository, error) {
	w := domain.WikiOf(repo)
	if c.StorageConfig.RepositoryExists(w.StorageKey) {
		return w, nil
	}
	if !create {
		return nil, ErrGitNotFound
	}
	if err := c.StorageConfig.InitRepository(w.StorageKey); err != nil && !c.StorageConfig.RepositoryExists(w.StorageKey) {
		return nil, err
	}
	return w, nil
}

// WikiPage is a page as read.
type WikiPage struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Path    string `json:"path"`
	Content string `json:"content"`
	// Commit is the last commit that changed the page.
	Commit *gitbrowse.Commit `json:"commit,omitempty"`
}

// WikiPageRef names a page in lists.
type WikiPageRef struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Path  string `json:"path"`
}

// WikiView is a page with its surroundings.
type WikiView struct {
	Page    *WikiPage `json:"page"`
	Sidebar *WikiPage `json:"sidebar,omitempty"`
	Footer  *WikiPage `json:"footer,omitempty"`
	// Head is the wiki's tip when it was read: the base of an edit.
	Head string `json:"head"`
}

// WikiInfo describes a repository's wiki for the caller.
type WikiInfo struct {
	Access  domain.WikiAccess `json:"access"`
	Exists  bool              `json:"exists"`
	CanEdit bool              `json:"can_edit"`
	Head    string            `json:"head,omitempty"`
	Pages   []WikiPageRef     `json:"pages"`
	// Truncated: more pages than listed.
	Truncated bool `json:"truncated,omitempty"`
}

// open returns the repository (if readerId may read it and its wiki is on)
// and its wiki's storer (nil when the wiki doesn't exist yet).
func (c *WikiApp) open(id, repoPath *string, readerId string) (*domain.Repository, storer.Storer, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, repoPath, readerId)
	if err != nil {
		return nil, nil, err
	}
	if repo.WikiAccess == domain.WikiOff {
		return nil, nil, ErrWikiNotFound
	}
	w := domain.WikiOf(repo)
	if !c.Storage.RepositoryExists(w.StorageKey) {
		return repo, nil, nil
	}
	st, err := c.Storage.GetStorer(w.StorageKey)
	if err != nil {
		return nil, nil, err
	}
	return repo, st, nil
}

// canEdit says whether userId may edit repo's wiki (nil) or why not.
func (c *WikiApp) canEdit(repo *domain.Repository, userId string) error {
	if !domain.IsPerson(userId) {
		return ErrForbidden
	}
	if repo.IsArchived {
		return ErrGitArchived
	}
	if repo.IsMirror {
		return ErrGitMirror
	}
	if repo.WikiAccess == domain.WikiReaders {
		return nil // open() checked read access
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, userId, "write")
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// head is the wiki's default branch and its tip (nil when empty).
func wikiHead(st storer.Storer) (plumbing.ReferenceName, *object.Commit, error) {
	branch := plumbing.Main
	if head, err := st.Reference(plumbing.HEAD); err == nil && head.Type() == plumbing.SymbolicReference {
		branch = head.Target()
	}
	ref, err := st.Reference(branch)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return branch, nil, nil
	} else if err != nil {
		return "", nil, err
	}
	commit, err := object.GetCommit(st, ref.Hash())
	if err != nil {
		return "", nil, err
	}
	return branch, commit, nil
}

// Info is the wiki's settings for the caller and its pages.
func (c *WikiApp) Info(id, repoPath *string, readerId string) (*WikiInfo, error) {
	repo, st, err := c.open(id, repoPath, readerId)
	if err != nil {
		return nil, err
	}
	info := &WikiInfo{Access: repo.WikiAccess, CanEdit: c.canEdit(repo, readerId) == nil, Pages: []WikiPageRef{}}
	if st == nil {
		return info, nil
	}
	_, head, err := wikiHead(st)
	if err != nil || head == nil {
		return info, err
	}
	info.Exists, info.Head = true, head.Hash.String()
	info.Pages, info.Truncated, err = wikiPages(head, wikiMaxPages)
	return info, err
}

// wikiPages lists the pages of commit (at most max), sorted by name.
func wikiPages(commit *object.Commit, max int) ([]WikiPageRef, bool, error) {
	tree, err := commit.Tree()
	if err != nil {
		return nil, false, err
	}
	pages := []WikiPageRef{}
	truncated := false
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, err := walker.Next()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, false, err
		}
		if !entry.Mode.IsFile() || name == wikiUploads || strings.HasPrefix(name, wikiUploads+"/") {
			continue
		}
		page, ok := pageName(name)
		if !ok {
			continue
		}
		if len(pages) == max {
			truncated = true
			break
		}
		pages = append(pages, WikiPageRef{Name: page, Title: pageTitle(page), Path: name})
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Name < pages[j].Name })
	return pages, truncated, nil
}

// pageName is the page a file is (its path without .md or .markdown).
func pageName(file string) (string, bool) {
	for _, ext := range []string{".md", ".markdown"} {
		if name, ok := strings.CutSuffix(file, ext); ok && name != "" && !strings.HasSuffix(name, "/") {
			return name, true
		}
	}
	return "", false
}

func pageTitle(name string) string {
	return strings.ReplaceAll(path.Base(name), "-", " ")
}

// cleanPageName normalizes a page name given by an editor: spaces become
// dashes, a trailing .md is dropped.
func cleanPageName(name string) (string, error) {
	name = strings.TrimSuffix(strings.Trim(strings.TrimSpace(name), "/"), ".md")
	segs := strings.Split(name, "/")
	if name == "" || len(name) > wikiMaxName || len(segs) > wikiMaxDepth || segs[0] == wikiUploads {
		return "", ErrWikiInvalidName
	}
	for i, s := range segs {
		s = strings.Join(strings.Fields(s), "-")
		if s == "" || strings.HasPrefix(s, ".") || strings.ContainsAny(s, "\\:*?\"<>|#%") || strings.ContainsFunc(s, unicode.IsControl) {
			return "", ErrWikiInvalidName
		}
		segs[i] = s
	}
	return strings.Join(segs, "/"), nil
}

// pagePath finds the file of page name in commit ("" when there is none).
func pagePath(commit *object.Commit, name string) (string, error) {
	if commit == nil {
		return "", nil
	}
	for _, ext := range []string{".md", ".markdown"} {
		if _, err := gitbrowse.OpenFile(commit, name+ext); err == nil {
			return name + ext, nil
		} else if !errors.Is(err, gitbrowse.ErrPathNotFound) && !errors.Is(err, gitbrowse.ErrNotAFile) {
			return "", err
		}
	}
	return "", nil
}

func readPage(commit *object.Commit, name string, withCommit bool) (*WikiPage, error) {
	file, err := pagePath(commit, name)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return nil, ErrWikiPageNotFound
	}
	f, err := gitbrowse.ReadFile(commit, file, wikiMaxPageBytes)
	if err != nil {
		return nil, err
	}
	page := &WikiPage{Name: name, Title: pageTitle(name), Path: file}
	if !f.Binary {
		page.Content = f.Content
	}
	if withCommit {
		if log, _, err := gitbrowse.Log(commit, file, 0, 1); err == nil && len(log) > 0 {
			page.Commit = &log[0]
		}
	}
	return page, nil
}

// nearest reads special (_Sidebar, _Footer) from the folder of name or
// the closest folder above it.
func nearest(commit *object.Commit, name, special string) *WikiPage {
	dir := path.Dir(name)
	for {
		candidate := special
		if dir != "." && dir != "/" {
			candidate = dir + "/" + special
		}
		if p, err := readPage(commit, candidate, false); err == nil {
			return p
		}
		if dir == "." || dir == "/" {
			return nil
		}
		dir = path.Dir(dir)
	}
}

// Page reads a page (Home when name is empty) at the wiki's tip, or at
// version (a commit hash) when given.
func (c *WikiApp) Page(id, repoPath *string, readerId, name, version string) (*WikiView, error) {
	_, st, err := c.open(id, repoPath, readerId)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, ErrWikiPageNotFound
	}
	_, head, err := wikiHead(st)
	if err != nil {
		return nil, err
	}
	if head == nil {
		return nil, ErrWikiPageNotFound
	}
	commit := head
	if version != "" {
		if commit, err = gitbrowse.GetCommit(st, version); err != nil {
			return nil, ErrWikiPageNotFound
		}
	}
	if name == "" {
		name = "Home"
	}
	page, err := readPage(commit, strings.Trim(name, "/"), true)
	if err != nil {
		return nil, err
	}
	view := &WikiView{Page: page, Head: head.Hash.String()}
	if !strings.HasPrefix(path.Base(page.Name), "_") {
		view.Sidebar = nearest(commit, page.Name, "_Sidebar")
		view.Footer = nearest(commit, page.Name, "_Footer")
	}
	return view, nil
}

// WikiEdit is a change to one page.
type WikiEdit struct {
	Name string // the page (as named by the editor)
	// NewName renames the page (empty: keep).
	NewName string
	Content string
	Message string
	// New: create the page (it must not exist).
	New bool
	// Base is the wiki's tip the edit started from; "" skips the conflict
	// check (the edit overwrites).
	Base string
	// Delete removes the page (Content is ignored).
	Delete bool
}

// WikiChange is what an edit did.
type WikiChange struct {
	Commit string `json:"commit"`
	Name   string `json:"name"` // the page's name after the change ("" when deleted)
}

// Save creates, edits, renames or deletes a page.
func (c *WikiApp) Save(id, repoPath *string, userId string, e WikiEdit) (*WikiChange, error) {
	repo, _, err := c.open(id, repoPath, userId)
	if err != nil {
		return nil, err
	}
	if err := c.canEdit(repo, userId); err != nil {
		return nil, err
	}
	name, err := cleanPageName(e.Name)
	if err != nil {
		return nil, err
	}
	target := name
	if e.NewName != "" && !e.Delete {
		if target, err = cleanPageName(e.NewName); err != nil {
			return nil, err
		}
	}
	if len(e.Content) > wikiMaxPageBytes {
		return nil, ErrWikiTooLarge
	}
	content := strings.ReplaceAll(e.Content, "\r\n", "\n")
	return c.commit(repo, userId, e.Base, func(base, head *object.Commit) (map[string][]byte, string, error) {
		current, err := pagePath(head, name)
		if err != nil {
			return nil, "", err
		}
		switch {
		case e.New && current != "":
			return nil, "", ErrWikiPageExists
		case !e.New && current == "" && base != nil:
			return nil, "", ErrWikiConflict // deleted or renamed meanwhile
		case !e.New && current == "":
			return nil, "", ErrWikiPageNotFound
		case !e.New:
			if changed, err := fileChanged(base, head, current); err != nil || changed {
				return nil, "", errors.Join(ErrWikiConflict, err)
			}
		}
		files := map[string][]byte{}
		title := pageTitle(target)
		if e.Delete {
			files[current] = nil
			return files, defaultMessage(e.Message, "Deleted "+title), nil
		}
		file := target + ".md"
		if current != "" && target == name {
			file = current // keep .markdown files' extension
		}
		if target != name {
			if exists, err := pagePath(head, target); err != nil || exists != "" {
				return nil, "", errors.Join(ErrWikiPageExists, err)
			}
			files[current] = nil
		} else if current != "" {
			if data, err := fileBytes(head, current); err == nil && string(data) == content {
				return nil, "", nil // unchanged: no commit
			}
		}
		files[file] = []byte(content)
		msg := "Updated " + title
		switch {
		case current == "":
			msg = "Created " + title
		case target != name:
			msg = "Renamed " + pageTitle(name) + " to " + title
		}
		return files, defaultMessage(e.Message, msg), nil
	}, map[bool]string{true: "", false: target}[e.Delete])
}

// Revert writes page name as it was at version.
func (c *WikiApp) Revert(id, repoPath *string, userId, name, version, base string) (*WikiChange, error) {
	repo, st, err := c.open(id, repoPath, userId)
	if err != nil {
		return nil, err
	}
	if err := c.canEdit(repo, userId); err != nil {
		return nil, err
	}
	if st == nil {
		return nil, ErrWikiPageNotFound
	}
	old, err := gitbrowse.GetCommit(st, version)
	if err != nil {
		return nil, ErrWikiPageNotFound
	}
	name = strings.Trim(name, "/")
	if name == "" {
		return nil, ErrWikiInvalidName
	}
	short := old.Hash.String()[:7]
	return c.commit(repo, userId, base, func(baseC, head *object.Commit) (map[string][]byte, string, error) {
		current, err := pagePath(head, name)
		if err != nil {
			return nil, "", err
		}
		if current != "" {
			if changed, err := fileChanged(baseC, head, current); err != nil || changed {
				return nil, "", errors.Join(ErrWikiConflict, err)
			}
		}
		then, err := pagePath(old, name)
		if err != nil {
			return nil, "", err
		}
		files := map[string][]byte{}
		if current != "" && current != then {
			files[current] = nil
		}
		msg := "Reverted " + pageTitle(name) + " to " + short
		if then == "" {
			if current == "" {
				return nil, "", ErrWikiPageNotFound
			}
			return files, msg, nil
		}
		data, err := fileBytes(old, then)
		if err != nil {
			return nil, "", err
		}
		files[then] = data
		return files, msg, nil
	}, name)
}

func defaultMessage(msg, fallback string) string {
	if strings.TrimSpace(msg) == "" {
		return fallback
	}
	return msg
}

func fileBytes(commit *object.Commit, file string) ([]byte, error) {
	f, err := gitbrowse.OpenFile(commit, file)
	if err != nil {
		return nil, err
	}
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// fileChanged says whether file differs between base and head (false
// without a base: no check).
func fileChanged(base, head *object.Commit, file string) (bool, error) {
	if base == nil {
		return false, nil
	}
	if head == nil {
		return true, nil
	}
	if base.Hash == head.Hash {
		return false, nil
	}
	hashAt := func(c *object.Commit) (plumbing.Hash, error) {
		tree, err := c.Tree()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		e, err := tree.FindEntry(file)
		if err != nil {
			return plumbing.ZeroHash, nil // absent
		}
		return e.Hash, nil
	}
	a, err := hashAt(base)
	if err != nil {
		return false, err
	}
	b, err := hashAt(head)
	return a != b, err
}

type wikiPageChange struct {
	name, action string
}

// commit applies build's files on the wiki's tip and moves the branch,
// retrying when someone else moved it meanwhile. build gets the commit
// the editor started from (base) and the tip.
func (c *WikiApp) commit(repo *domain.Repository, userId, baseHash string,
	build func(base, head *object.Commit) (map[string][]byte, string, error), name string) (*WikiChange, error) {
	wiki, err := c.Repositories.wikiFor(repo, true)
	if err != nil {
		return nil, err
	}
	st, err := c.Storage.GetStorer(wiki.StorageKey)
	if err != nil {
		return nil, err
	}
	user, err := c.Users.GetById(userId)
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		branch, head, err := wikiHead(st)
		if err != nil {
			return nil, err
		}
		var base *object.Commit
		if baseHash != "" {
			if base, err = gitbrowse.GetCommit(st, baseHash); err != nil {
				return nil, ErrWikiConflict
			}
		}
		files, msg, err := build(base, head)
		if err != nil {
			return nil, err
		}
		if files == nil && head != nil {
			return &WikiChange{Commit: head.Hash.String(), Name: name}, nil
		}
		sig := object.Signature{Name: user.Username, Email: user.Email, When: c.clock()}
		res, err := gitmerge.Edit(st, head, files, sig, msg)
		if err != nil {
			return nil, err
		}
		if err := gitmerge.Write(st, res); err != nil {
			return nil, err
		}
		ref := plumbing.NewHashReference(branch, res.Commit)
		if head == nil {
			if creator, ok := st.(helpergit.ReferenceCreator); ok {
				err = creator.CreateReference(ref)
			} else {
				err = st.SetReference(ref)
			}
		} else {
			err = st.CheckAndSetReference(ref, plumbing.NewHashReference(branch, head.Hash))
		}
		if err != nil {
			if attempt+1 < wikiCASRetries {
				continue // moved meanwhile: build again on the new tip
			}
			return nil, ErrWikiConflict
		}
		// Like a push: gollum, notifications, push mirrors.
		update := RefUpdate{Name: branch, New: res.Commit}
		if head != nil {
			update.Old = head.Hash
		}
		if c.Webhooks != nil {
			c.Webhooks.Pushed(wiki, Pusher{UserId: user.Id, Name: user.Username}, st, []RefUpdate{update})
		} else {
			c.Pushed(wiki, Pusher{UserId: user.Id, Name: user.Username}, st, []RefUpdate{update})
		}
		return &WikiChange{Commit: res.Commit.String(), Name: name}, nil
	}
}

// Pushed handles a push to a wiki (git): the default branch's changed
// pages are announced. A wiki made by its first push may have HEAD on a
// branch that doesn't exist (main, when master was pushed): HEAD then
// follows the pushed branch.
func (c *WikiApp) Pushed(wiki *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer, updates []RefUpdate) {
	if c == nil {
		return
	}
	st, ok := objects.(storer.Storer)
	if !ok {
		return
	}
	branch, head, err := wikiHead(st)
	if err != nil {
		return
	}
	if head == nil {
		for _, u := range updates {
			if u.Name.IsBranch() && !u.New.IsZero() {
				if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, u.Name)); err == nil {
					branch = u.Name
				}
				break
			}
		}
	}
	for _, u := range updates {
		if u.Name != branch || u.New.IsZero() {
			continue
		}
		pages, err := changedPages(st, u.Old, u.New)
		if err != nil {
			c.Logger.Warn().Err(err).Str("component", "application.wiki").Msg("failed to diff a wiki push")
			continue
		}
		c.changed(wiki, pusher, u.New, pages)
	}
}

// changedPages are the pages created or edited from old to new (deletions
// aren't events, as on GitHub).
func changedPages(st storer.EncodedObjectStorer, old, new plumbing.Hash) ([]wikiPageChange, error) {
	to, err := object.GetCommit(st, new)
	if err != nil {
		return nil, err
	}
	toTree, err := to.Tree()
	if err != nil {
		return nil, err
	}
	fromTree := &object.Tree{}
	if !old.IsZero() {
		if from, err := object.GetCommit(st, old); err == nil {
			if fromTree, err = from.Tree(); err != nil {
				return nil, err
			}
		}
	}
	changes, err := object.DiffTree(fromTree, toTree)
	if err != nil {
		return nil, err
	}
	var pages []wikiPageChange
	for _, ch := range changes {
		if ch.To.Name == "" || ch.To.TreeEntry.Mode == filemode.Dir || strings.HasPrefix(ch.To.Name, wikiUploads+"/") {
			continue
		}
		name, ok := pageName(ch.To.Name)
		if !ok {
			continue
		}
		action := "edited"
		if ch.From.Name == "" {
			action = "created"
		}
		pages = append(pages, wikiPageChange{name: name, action: action})
		if len(pages) == 100 {
			break
		}
	}
	return pages, nil
}

// changed sends the gollum event and notifications for pages.
func (c *WikiApp) changed(wiki *domain.Repository, pusher Pusher, commit plumbing.Hash, pages []wikiPageChange) {
	if len(pages) == 0 {
		return
	}
	c.Webhooks.Gollum(wiki, pusher, commit, pages)
	if c.Notifications == nil {
		return
	}
	actor := pusher.Name
	if pusher.UserId != "" {
		actor = c.Notifications.ActorName(pusher.UserId)
	}
	summary := fmt.Sprintf("%s edited %d wiki pages", actor, len(pages))
	pagePath := "/" + wiki.FullPath() + "/wiki"
	if len(pages) == 1 {
		verb := map[string]string{"created": "created", "edited": "edited"}[pages[0].action]
		summary = actor + " " + verb + " the wiki page " + pageTitle(pages[0].name)
		pagePath += "/" + escapePath(pages[0].name)
	}
	c.Notifications.Notify(Event{Repo: wiki, ThreadType: domain.ThreadWiki, ThreadId: wiki.Id, Title: "Wiki",
		Path: pagePath, ActorId: pusher.UserId, Summary: summary, WatchLevels: []domain.WatchLevel{domain.WatchAll}})
}

// WikiSearchResult is a page that matched.
type WikiSearchResult struct {
	WikiPageRef
	Snippet string `json:"snippet"`
}

// Search finds the pages containing every word of query.
func (c *WikiApp) Search(id, repoPath *string, readerId, query string) ([]WikiSearchResult, error) {
	_, st, err := c.open(id, repoPath, readerId)
	if err != nil {
		return nil, err
	}
	results := []WikiSearchResult{}
	words := strings.Fields(strings.ToLower(query))
	if st == nil || len(words) == 0 {
		return results, nil
	}
	_, head, err := wikiHead(st)
	if err != nil || head == nil {
		return results, err
	}
	pages, _, err := wikiPages(head, wikiSearchPages)
	if err != nil {
		return nil, err
	}
	budget := int64(wikiSearchBytes)
	for _, p := range pages {
		if budget <= 0 || len(results) == wikiSearchResults {
			break
		}
		f, err := gitbrowse.ReadFile(head, p.Path, wikiMaxPageBytes)
		if err != nil || f.Binary {
			continue
		}
		budget -= int64(len(f.Content))
		text := strings.ToLower(p.Title + "\n" + f.Content)
		all := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				all = false
				break
			}
		}
		if all {
			results = append(results, WikiSearchResult{WikiPageRef: p, Snippet: snippet(f.Content, words[0])})
		}
	}
	return results, nil
}

// snippet is about 160 characters of content around word.
func snippet(content, word string) string {
	runes := []rune(content)
	lower := []rune(strings.ToLower(content))
	at := strings.Index(string(lower), word)
	start := 0
	if at > 0 {
		start = max(len([]rune(string(lower)[:at]))-60, 0)
	}
	end := min(start+160, len(runes))
	s := strings.Join(strings.Fields(string(runes[start:end])), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(runes) {
		s += "…"
	}
	return s
}

// WikiAttachment is an uploaded file.
type WikiAttachment struct {
	Path   string `json:"path"` // in the wiki (link it relative to pages at the root)
	Commit string `json:"commit"`
}

// MaxAttachmentSize is the attachment limit.
func (c *WikiApp) MaxAttachmentSize() int64 {
	if c.MaxAttachment > 0 {
		return c.MaxAttachment
	}
	return wikiDefaultMaxFile
}

// Attach stores a file (at most MaxAttachmentSize) as an LFS object and
// commits its pointer under uploads/.
func (c *WikiApp) Attach(id, repoPath *string, userId, fileName string, body io.Reader) (*WikiAttachment, error) {
	repo, _, err := c.open(id, repoPath, userId)
	if err != nil {
		return nil, err
	}
	if err := c.canEdit(repo, userId); err != nil {
		return nil, err
	}
	fileName = strings.Join(strings.Fields(pathBase(fileName)), "-")
	if fileName == "" || strings.HasPrefix(fileName, ".") || strings.ContainsAny(fileName, "\\:*?\"<>|#%") ||
		strings.ContainsFunc(fileName, unicode.IsControl) || len(fileName) > wikiMaxName {
		return nil, ErrWikiInvalidFile
	}
	tmp, err := os.CreateTemp("", "ogit-wiki-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	h := sha256.New()
	limit := c.MaxAttachmentSize()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if n == 0 || n > limit {
		return nil, ErrWikiInvalidFile
	}
	oid := hex.EncodeToString(h.Sum(nil))
	wiki, err := c.Repositories.wikiFor(repo, true)
	if err != nil {
		return nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := c.Storage.PutLFSObject(wiki.StorageKey, oid, tmp); err != nil {
		return nil, err
	}
	var random [6]byte
	_, _ = rand.Read(random[:])
	file := wikiUploads + "/" + hex.EncodeToString(random[:]) + "/" + fileName
	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, n)
	change, err := c.commit(repo, userId, "", func(_, head *object.Commit) (map[string][]byte, string, error) {
		files := map[string][]byte{file: []byte(pointer)}
		// So that git clients check attachments out as files, not pointers.
		if head == nil {
			files[".gitattributes"] = []byte(wikiAttributes)
		} else if _, err := gitbrowse.OpenFile(head, ".gitattributes"); errors.Is(err, gitbrowse.ErrPathNotFound) {
			files[".gitattributes"] = []byte(wikiAttributes)
		}
		return files, "Uploaded " + fileName, nil
	}, "")
	if err != nil {
		return nil, err
	}
	return &WikiAttachment{Path: file, Commit: change.Commit}, nil
}

const wikiAttributes = wikiUploads + "/** filter=lfs diff=lfs merge=lfs -text\n"

func pathBase(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}
