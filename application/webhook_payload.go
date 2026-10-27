package application

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
)

// Payloads follow GitHub's webhook payloads, with oGit ids (strings) and
// URLs built from http.public_url.

type HookOwner struct {
	Login   string `json:"login"`
	Id      string `json:"id"`
	Type    string `json:"type"` // User or Organization (a group)
	HTMLURL string `json:"html_url"`
}

type HookRepository struct {
	Id            string    `json:"id"`
	Name          string    `json:"name"`
	FullName      string    `json:"full_name"`
	Private       bool      `json:"private"`
	Owner         HookOwner `json:"owner"`
	HTMLURL       string    `json:"html_url"`
	Description   string    `json:"description"`
	Fork          bool      `json:"fork"`
	URL           string    `json:"url"`
	CloneURL      string    `json:"clone_url"`
	SSHURL        string    `json:"ssh_url,omitempty"`
	DefaultBranch string    `json:"default_branch"`
	MasterBranch  string    `json:"master_branch"`
	Archived      bool      `json:"archived"`
	Visibility    string    `json:"visibility"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type HookUser struct {
	Login     string `json:"login"`
	Id        string `json:"id,omitempty"`
	Type      string `json:"type"`
	HTMLURL   string `json:"html_url,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type HookGitUser struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Username string `json:"username,omitempty"`
}

type HookCommit struct {
	Id        string      `json:"id"`
	TreeId    string      `json:"tree_id"`
	Distinct  bool        `json:"distinct"`
	Message   string      `json:"message"`
	Timestamp time.Time   `json:"timestamp"`
	URL       string      `json:"url"`
	Author    HookGitUser `json:"author"`
	Committer HookGitUser `json:"committer"`
	Added     []string    `json:"added"`
	Removed   []string    `json:"removed"`
	Modified  []string    `json:"modified"`
}

type PushPayload struct {
	Ref        string         `json:"ref"`
	Before     string         `json:"before"`
	After      string         `json:"after"`
	Created    bool           `json:"created"`
	Deleted    bool           `json:"deleted"`
	Forced     bool           `json:"forced"`
	BaseRef    *string        `json:"base_ref"`
	Compare    string         `json:"compare"`
	Commits    []HookCommit   `json:"commits"`
	HeadCommit *HookCommit    `json:"head_commit"`
	Repository HookRepository `json:"repository"`
	Pusher     HookGitUser    `json:"pusher"`
	Sender     HookUser       `json:"sender"`
}

type PingPayload struct {
	Zen        string          `json:"zen"`
	HookId     string          `json:"hook_id"`
	Hook       PingHook        `json:"hook"`
	Repository *HookRepository `json:"repository,omitempty"`
	// Organization: a group hook's group.
	Organization *HookOwner `json:"organization,omitempty"`
	Sender       HookUser   `json:"sender"`
}

type PingHook struct {
	Id     string         `json:"id"`
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Active bool           `json:"active"`
	Events []string       `json:"events"`
	Config PingHookConfig `json:"config"`
}

type PingHookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	InsecureSSL string `json:"insecure_ssl"`
}

type RepositoryPayload struct {
	Action     string         `json:"action"`
	Changes    map[string]any `json:"changes,omitempty"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

// maxPushCommits is how many commits a push payload lists, like GitHub's 20.
const maxPushCommits = 20

func (c *WebhookApp) baseURL() string {
	return strings.TrimRight(c.Config.Server.PublicURL, "/")
}

func (c *WebhookApp) repositoryPayload(repo *domain.Repository) HookRepository {
	base := c.baseURL()
	html := base + "/" + repo.FullPath()
	owner := HookOwner{Login: repo.Namespace, Id: repo.OwnerId, Type: hookOwnerType(repo.OwnerType), HTMLURL: base + "/" + repo.Namespace}
	r := HookRepository{
		Id: repo.Id, Name: repo.Slug, FullName: repo.FullPath(), Private: repo.IsPrivate, Owner: owner,
		HTMLURL: html, Description: repo.Description, URL: base + "/api/v1/repository/" + url.PathEscape(repo.FullPath()),
		CloneURL: html + ".git", DefaultBranch: repo.DefaultBranch, MasterBranch: repo.DefaultBranch,
		Archived: repo.IsArchived, Visibility: "public", CreatedAt: repo.CreatedAt, UpdatedAt: repo.UpdatedAt,
	}
	if repo.IsPrivate {
		r.Visibility = "private"
	}
	if c.Config.SSH.Enabled {
		host := base
		if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
			host = u.Hostname()
		}
		port := ""
		if c.Config.SSH.Port != 22 && c.Config.SSH.Port != 0 {
			port = fmt.Sprintf(":%d", c.Config.SSH.Port)
		}
		r.SSHURL = "ssh://git@" + host + port + "/" + repo.FullPath() + ".git"
	}
	return r
}

func hookOwnerType(t domain.OwnerType) string {
	if t == domain.OwnerGroup {
		return "Organization"
	}
	return "User"
}

// sender describes a user by id; a missing user (deploy keys and
// repository tokens have none) is described by name only.
func (c *WebhookApp) sender(userId, name string) HookUser {
	if userId != "" && c.Users != nil {
		if u, err := c.Users.GetById(userId); err == nil {
			return HookUser{Login: u.Username, Id: u.Id, Type: "User", HTMLURL: c.baseURL() + "/" + u.Username, AvatarURL: u.AvatarUrl}
		}
	}
	return HookUser{Login: name, Type: "Bot"}
}

func (c *WebhookApp) pingPayload(s WebhookScope, h *domain.Webhook, actorId string) PingPayload {
	p := PingPayload{
		Zen: "Keep it logically awesome.", HookId: h.Id,
		Hook: PingHook{Id: h.Id, Type: "Repository", Name: "web", Active: h.Active, Events: h.Events,
			Config: PingHookConfig{URL: h.URL, ContentType: "json", InsecureSSL: "0"}},
		Sender: c.sender(actorId, ""),
	}
	if s.Group != nil {
		p.Hook.Type = "Organization"
		p.Organization = &HookOwner{Login: s.Group.Name, Id: s.Group.Id, Type: "Organization"}
	} else {
		repo := c.repositoryPayload(s.Repo)
		p.Repository = &repo
	}
	return p
}

// Pusher is who pushed: a user, or a deploy key / repository token (no
// UserId).
type Pusher struct {
	UserId string
	Name   string
	// OnlyBranches, when set, are the only branches this push may change
	// (maintainer edits on a fork).
	OnlyBranches []string
	// JobToken: an Actions job pushed (its pushes start no workflows).
	JobToken bool
}

// LimitPush wraps next (nil: none) to refuse references outside
// pusher.OnlyBranches, when set.
func LimitPush(pusher Pusher, next func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error) func(plumbing.ReferenceName, plumbing.Hash, plumbing.Hash) error {
	if pusher.OnlyBranches == nil {
		return next
	}
	return func(name plumbing.ReferenceName, old, new plumbing.Hash) error {
		if !name.IsBranch() || !slices.Contains(pusher.OnlyBranches, name.Short()) || new.IsZero() {
			return fmt.Errorf("you may only push to the source branches of pull requests that allow edits by maintainers (%s)", strings.Join(pusher.OnlyBranches, ", "))
		}
		if next != nil {
			return next(name, old, new)
		}
		return nil
	}
}

// RefUpdate is a reference a push (or a merge) changed. A zero Old is a
// creation, a zero New a deletion.
type RefUpdate struct {
	Name     plumbing.ReferenceName
	Old, New plumbing.Hash
}

// AppliedUpdates returns the commands of req that report says succeeded.
func AppliedUpdates(req *packp.ReferenceUpdateRequest, report *packp.ReportStatus) []RefUpdate {
	if req == nil || report == nil || report.UnpackStatus != "ok" {
		return nil
	}
	ok := map[plumbing.ReferenceName]bool{}
	for _, st := range report.CommandStatuses {
		ok[st.ReferenceName] = st.Status == "ok"
	}
	var out []RefUpdate
	for _, cmd := range req.Commands {
		if ok[cmd.Name] {
			out = append(out, RefUpdate{Name: cmd.Name, Old: cmd.Old, New: cmd.New})
		}
	}
	return out
}

// Pushed queues push events for updates, reading commits from objects.
func (c *WebhookApp) Pushed(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer, updates []RefUpdate) {
	if c == nil || len(updates) == 0 {
		return
	}
	c.PushMirrors.Pushed(repo)
	c.Actions.Pushed(repo, pusher, objects, updates)
	hooks, err := c.hooksFor(repo)
	if err != nil {
		return
	}
	if anyWants(hooks, "push") { // otherwise skip walking commits
		for _, u := range updates {
			c.emit(repo, "push", "", c.pushPayload(repo, pusher, objects, u))
		}
	}
	if anyWants(hooks, "pull_request") || c.Actions != nil {
		if refs, ok := objects.(storer.ReferenceStorer); ok {
			c.synchronize(repo, pusher, refs, updates)
		}
	}
}

func anyWants(hooks []domain.Webhook, event string) bool {
	for _, h := range hooks {
		if h.Active && h.Wants(event) {
			return true
		}
	}
	return false
}

// PushPayloadFor is the push event's payload (also the push event of
// Actions runs).
func (c *WebhookApp) PushPayloadFor(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer, u RefUpdate) PushPayload {
	return c.pushPayload(repo, pusher, objects, u)
}

func (c *WebhookApp) pushPayload(repo *domain.Repository, pusher Pusher, objects storer.EncodedObjectStorer, u RefUpdate) PushPayload {
	html := c.baseURL() + "/" + repo.FullPath()
	p := PushPayload{
		Ref: u.Name.String(), Before: u.Old.String(), After: u.New.String(),
		Created: u.Old.IsZero(), Deleted: u.New.IsZero(), Commits: []HookCommit{},
		Repository: c.repositoryPayload(repo), Sender: c.sender(pusher.UserId, pusher.Name),
		Pusher: HookGitUser{Name: pusher.Name},
	}
	if pusher.UserId != "" {
		p.Pusher = HookGitUser{Name: p.Sender.Login, Username: p.Sender.Login}
		if user, err := c.Users.GetById(pusher.UserId); err == nil {
			p.Pusher.Email = user.Email
		}
	}
	short := func(h plumbing.Hash) string { return h.String()[:12] }
	switch {
	case p.Created:
		p.Compare = html + "/commit/" + u.New.String()
	case p.Deleted:
		p.Compare = html + "/compare/" + short(u.Old) + "..." + short(u.Old)
	default:
		p.Compare = html + "/compare/" + short(u.Old) + "..." + short(u.New)
	}
	if p.Deleted {
		return p
	}
	stop := u.Old
	if p.Created {
		// A new branch lists what it adds to the default branch, not its
		// whole history.
		stop = forkPoint(objects, repo, u)
	}
	commits, reachedOld := pushCommits(objects, u.New, stop)
	if !p.Created && !reachedOld {
		p.Forced = !isAncestor(objects, u.Old, u.New)
	}
	for i := len(commits) - 1; i >= 0; i-- { // oldest first, like GitHub
		p.Commits = append(p.Commits, c.hookCommit(html, commits[i]))
	}
	if head, err := object.GetCommit(objects, u.New); err == nil {
		hc := c.hookCommit(html, head)
		p.HeadCommit = &hc
	}
	return p
}

// forkPoint returns where a new branch leaves the default branch (zero
// when it is the default branch, or unrelated to it).
func forkPoint(objects storer.EncodedObjectStorer, repo *domain.Repository, u RefUpdate) plumbing.Hash {
	def := plumbing.NewBranchReferenceName(repo.DefaultBranch)
	refs, ok := objects.(storer.ReferenceStorer)
	if repo.DefaultBranch == "" || u.Name == def || !ok {
		return plumbing.ZeroHash
	}
	ref, err := refs.Reference(def)
	if err != nil {
		return plumbing.ZeroHash
	}
	tip, err1 := object.GetCommit(objects, u.New)
	base, err2 := object.GetCommit(objects, ref.Hash())
	if err1 != nil || err2 != nil {
		return plumbing.ZeroHash
	}
	bases, err := tip.MergeBase(base)
	if err != nil || len(bases) == 0 {
		return plumbing.ZeroHash
	}
	return bases[0].Hash
}

// pushCommits returns up to maxPushCommits commits reachable from tip and
// not past old, newest first, and whether old was reached.
func pushCommits(objects storer.EncodedObjectStorer, tip, old plumbing.Hash) ([]*object.Commit, bool) {
	head, err := object.GetCommit(objects, tip)
	if err != nil {
		return nil, false
	}
	var out []*object.Commit
	reached := false
	seen := map[plumbing.Hash]bool{}
	queue := []*object.Commit{head}
	for len(queue) > 0 && len(out) < maxPushCommits {
		cm := queue[0]
		queue = queue[1:]
		if seen[cm.Hash] {
			continue
		}
		seen[cm.Hash] = true
		if cm.Hash == old {
			reached = true
			continue
		}
		out = append(out, cm)
		_ = cm.Parents().ForEach(func(p *object.Commit) error {
			queue = append(queue, p)
			return nil
		})
	}
	for _, q := range queue {
		if q.Hash == old {
			reached = true
		}
	}
	return out, reached
}

var errFound = errors.New("found")

// isAncestor reports whether a is reachable from b. It walks b's history,
// so it only runs for pushes too long to list or rewritten ones.
func isAncestor(objects storer.EncodedObjectStorer, a, b plumbing.Hash) bool {
	tip, err := object.GetCommit(objects, b)
	if err != nil {
		return false
	}
	err = object.NewCommitPreorderIter(tip, nil, nil).ForEach(func(cm *object.Commit) error {
		if cm.Hash == a {
			return errFound
		}
		return nil
	})
	return errors.Is(err, errFound)
}

func (c *WebhookApp) hookCommit(html string, cm *object.Commit) HookCommit {
	hc := HookCommit{
		Id: cm.Hash.String(), TreeId: cm.TreeHash.String(), Distinct: true, Message: cm.Message,
		Timestamp: cm.Committer.When, URL: html + "/commit/" + cm.Hash.String(),
		Author:    HookGitUser{Name: cm.Author.Name, Email: cm.Author.Email},
		Committer: HookGitUser{Name: cm.Committer.Name, Email: cm.Committer.Email},
		Added:     []string{}, Removed: []string{}, Modified: []string{},
	}
	tree, err := cm.Tree()
	if err != nil {
		return hc
	}
	var parentTree *object.Tree
	if cm.NumParents() > 0 {
		if parent, err := cm.Parent(0); err == nil {
			parentTree, _ = parent.Tree()
		}
	}
	changes, err := object.DiffTree(parentTree, tree)
	if err != nil {
		return hc
	}
	for _, ch := range changes {
		switch {
		case ch.From.Name == "":
			hc.Added = append(hc.Added, ch.To.Name)
		case ch.To.Name == "":
			hc.Removed = append(hc.Removed, ch.From.Name)
		default:
			hc.Modified = append(hc.Modified, ch.To.Name)
		}
	}
	return hc
}

// ForkPayload is GitHub's "fork" event, sent to the original's hooks.
type ForkPayload struct {
	Forkee     HookRepository `json:"forkee"`
	Repository HookRepository `json:"repository"`
	Sender     HookUser       `json:"sender"`
}

// ForkEvent queues a fork event on src's hooks. As on GitHub, the
// original's maintainers learn about every fork, private ones included.
func (c *WebhookApp) ForkEvent(src, fork *domain.Repository, actorId string) {
	if c == nil {
		return
	}
	c.emit(src, "fork", "", ForkPayload{Forkee: c.repositoryPayload(fork), Repository: c.repositoryPayload(src), Sender: c.sender(actorId, "")})
}

// RepositoryEvent queues a repository event (renamed, transferred,
// publicized, privatized, archived, unarchived, edited).
func (c *WebhookApp) RepositoryEvent(repo *domain.Repository, action, actorId string, changes map[string]any) {
	if c == nil {
		return
	}
	c.emit(repo, "repository", action, RepositoryPayload{Action: action, Changes: changes, Repository: c.repositoryPayload(repo), Sender: c.sender(actorId, "")})
}
