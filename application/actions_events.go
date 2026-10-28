package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/gitmerge"
	"gopkg.in/yaml.v3"
)

// Events other than push:
//   - pull_request (types opened, synchronize, reopened by default; branch
//     filters match the base branch, path filters the files the pull
//     request changes) runs the workflows of refs/pull/<n>/merge, the head
//     merged into the base, or of the head when they conflict; statuses go
//     on the head. Runs from forks get no secrets and a read-only token;
//     a fork's first-time contributor (no merged pull request here) waits
//     for a maintainer's approval;
//   - issues, issue_comment, release, create and delete run the default
//     branch's workflows (`types:` filters the action);
//   - workflow_dispatch and schedule: see actions_dispatch.go.

// defaultTypes are the actions an event runs for without `types:`.
var defaultTypes = map[string][]string{
	"pull_request": {"opened", "synchronize", "reopened"},
}

// maxPathFilterFiles: with more changed files, path filters always match
// (as on GitHub).
const maxPathFilterFiles = 3000

// matchesTypes applies an event's `types:` to action.
func matchesTypes(cfg yaml.Node, event, action string) bool {
	types, ok := filters(cfg)["types"]
	if !ok {
		types = defaultTypes[event]
	}
	return len(types) == 0 || slices.Contains(types, action)
}

// matchesPaths applies `paths:` and `paths-ignore:` to the changed files
// (nil: unknown, they match).
func matchesPaths(cfg yaml.Node, changed []string) bool {
	f := filters(cfg)
	include, ignore := f["paths"], f["paths-ignore"]
	if changed == nil || (len(include) == 0 && len(ignore) == 0) {
		return true
	}
	for _, path := range changed {
		if matchPatterns(path, include, ignore) {
			return true
		}
	}
	return false
}

// changedPaths lists the files that differ between two commits (nil when
// unknown or too many).
func changedPaths(objects storer.EncodedObjectStorer, from, to plumbing.Hash) []string {
	if from.IsZero() || to.IsZero() {
		return nil
	}
	a, err1 := commitTree(objects, from)
	b, err2 := commitTree(objects, to)
	if err1 != nil || err2 != nil {
		return nil
	}
	changes, err := object.DiffTree(a, b)
	if err != nil || len(changes) > maxPathFilterFiles {
		return nil
	}
	out := make([]string, 0, len(changes))
	for _, ch := range changes {
		if ch.To.Name != "" {
			out = append(out, ch.To.Name)
		}
		if ch.From.Name != "" && ch.From.Name != ch.To.Name {
			out = append(out, ch.From.Name)
		}
	}
	return out
}

// eventSender reads who caused an event from its webhook payload.
func eventSender(payload []byte) (string, string) {
	var p struct {
		Sender HookUser `json:"sender"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.Sender.Id, p.Sender.Login
}

// Event queues the workflows a webhook event runs (called for every event
// WebhookApp emits). It never fails the action that caused it.
func (c *ActionsApp) Event(repo *domain.Repository, event, action string, payload any) {
	if c == nil {
		return
	}
	switch event {
	case "pull_request":
		if p, ok := payload.(PullRequestPayload); ok {
			c.pullRequest(repo, p)
		}
	case "issues", "issue_comment", "release", "gollum":
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		id, name := eventSender(data)
		c.onDefaultBranch(repo, event, action, data, id, name)
	}
}

// onDefaultBranch runs the default branch's workflows for event.
func (c *ActionsApp) onDefaultBranch(repo *domain.Repository, event, action string, payload []byte, actorId, actorName string) {
	logger := c.Logger.With().Str("component", "application.actions").Str("repository", repo.FullPath()).Str("event", event).Logger()
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return
	}
	ref := plumbing.NewBranchReferenceName(repo.DefaultBranch)
	tip, err := st.Reference(ref)
	if err != nil {
		return // empty repository
	}
	files, err := ReadWorkflows(st, tip.Hash())
	if err != nil {
		logger.Warn().Err(err).Msg("failed to read workflows")
		return
	}
	for _, f := range files {
		if f.Parsed == nil {
			continue
		}
		cfg, ok := f.Parsed.Events[event]
		if !ok || !matchesTypes(cfg, event, action) {
			continue
		}
		run := c.newRun(repo, f, event, string(payload), ref.String(), tip.Hash(), actorId, actorName)
		if err := c.Pers.CreateRun(run); err != nil {
			logger.Error().Err(err).Str("workflow", f.Path).Msg("failed to queue a run")
		}
	}
}

// refEvent queues create and delete events for a pushed ref.
func (c *ActionsApp) refEvent(repo *domain.Repository, pusher Pusher, u RefUpdate) {
	event := ""
	switch {
	case u.Old.IsZero() && !u.New.IsZero():
		event = "create"
	case !u.Old.IsZero() && u.New.IsZero():
		event = "delete"
	default:
		return
	}
	refType := "branch"
	if u.Name.IsTag() {
		refType = "tag"
	} else if !u.Name.IsBranch() {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"ref": u.Name.Short(), "ref_type": refType, "master_branch": repo.DefaultBranch, "pusher_type": "user",
		"repository": c.Webhooks.repositoryPayload(repo), "sender": c.Webhooks.sender(pusher.UserId, pusher.Name),
	})
	c.onDefaultBranch(repo, event, "", payload, pusher.UserId, pusher.Name)
}

func (c *ActionsApp) newRun(repo *domain.Repository, f WorkflowFile, event, payload, ref string, sha plumbing.Hash, actorId, actorName string) *domain.ActionRun {
	run := &domain.ActionRun{
		RepositoryId: repo.Id, WorkflowPath: f.Path, WorkflowName: f.Parsed.Name, Workflow: f.Content,
		Event: event, EventPayload: payload, Ref: ref, Sha: sha.String(), ActorName: actorName,
		Labels: joinLabels(f.Parsed.Labels), Status: domain.RunQueued, CreatedAt: c.now(),
		Permissions: f.Parsed.PermissionString(),
	}
	if run.WorkflowName == "" {
		run.WorkflowName = f.Path
	}
	if domain.IsPerson(actorId) {
		run.ActorId = &actorId
	}
	return run
}

// pullRequest queues the runs of a pull request event.
func (c *ActionsApp) pullRequest(repo *domain.Repository, p PullRequestPayload) {
	logger := c.Logger.With().Str("component", "application.actions").Str("repository", repo.FullPath()).Int("pull", p.Number).Logger()
	pr := p.PullRequest
	if pr.State != "open" && p.Action != "closed" {
		return
	}
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return
	}
	head := plumbing.NewHash(pr.Head.SHA)
	base, err := st.Reference(plumbing.NewBranchReferenceName(pr.Base.Ref))
	if err != nil {
		return
	}
	// Test the merge result, like GitHub, unless it conflicts.
	sha, ref := head, fmt.Sprintf("refs/pull/%d/head", pr.Number)
	if merged, err := c.mergeRef(st, pr.Number, base.Hash(), head); err == nil {
		sha, ref = merged, fmt.Sprintf("refs/pull/%d/merge", pr.Number)
	} else if !errors.As(err, new(*gitmerge.ConflictError)) && !errors.Is(err, gitmerge.ErrNothingToMerge) {
		logger.Warn().Err(err).Msg("failed to compute the merge ref")
	}
	files, err := ReadWorkflows(st, sha)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to read workflows")
		return
	}
	fromFork := pr.Head.Repo.Id != "" && pr.Head.Repo.Id != repo.Id
	needsApproval := false
	if fromFork {
		author := ""
		if pr.User != nil {
			author = pr.User.Id
		}
		merged, err := c.Pers.HasMergedPull(repo.Id, author)
		needsApproval = err != nil || !merged || author == ""
	}
	var changed []string
	if bases, err := mergeBase(st, base.Hash(), head); err == nil {
		changed = changedPaths(st, bases, head)
	}
	payload, _ := json.Marshal(p)
	for _, f := range files {
		if f.Parsed == nil {
			continue
		}
		cfg, ok := f.Parsed.Events["pull_request"]
		if !ok || !matchesTypes(cfg, "pull_request", p.Action) {
			continue
		}
		fl := filters(cfg)
		if !matchPatterns(pr.Base.Ref, fl["branches"], fl["branches-ignore"]) || !matchesPaths(cfg, changed) {
			continue
		}
		run := c.newRun(repo, f, "pull_request", string(payload), ref, sha, p.Sender.Id, p.Sender.Login)
		run.StatusSha, run.PullNumber = head.String(), pr.Number
		run.FromFork, run.NeedsApproval = fromFork, needsApproval
		if fromFork {
			run.Permissions = "contents:read"
		}
		if err := c.Pers.CreateRun(run); err != nil {
			logger.Error().Err(err).Str("workflow", f.Path).Msg("failed to queue a run")
		}
	}
}

func mergeBase(objects storer.EncodedObjectStorer, a, b plumbing.Hash) (plumbing.Hash, error) {
	ca, err := object.GetCommit(objects, a)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	cb, err := object.GetCommit(objects, b)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	bases, err := ca.MergeBase(cb)
	if err != nil || len(bases) == 0 {
		return plumbing.ZeroHash, errors.New("no merge base")
	}
	return bases[0].Hash, nil
}

// mergeRef writes head merged into base and points refs/pull/<n>/merge at
// it.
func (c *ActionsApp) mergeRef(st storer.Storer, number int, base, head plumbing.Hash) (plumbing.Hash, error) {
	bc, err := object.GetCommit(st, base)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	hc, err := object.GetCommit(st, head)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sig := object.Signature{Name: "oGit", Email: "noreply@ogit", When: c.now().Truncate(time.Second)}
	res, err := gitmerge.Merge(st, gitmerge.Request{Base: bc, Head: hc, Method: gitmerge.MethodMerge, Committer: sig,
		Message: fmt.Sprintf("Merge %s into %s", head.String(), base.String())})
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if err := gitmerge.Write(st, res); err != nil {
		return plumbing.ZeroHash, err
	}
	ref := plumbing.NewHashReference(plumbing.ReferenceName(fmt.Sprintf("refs/pull/%d/merge", number)), res.Commit)
	if err := st.SetReference(ref); err != nil {
		return plumbing.ZeroHash, err
	}
	return res.Commit, nil
}

// Approve lets a waiting run from a first-time contributor's fork run
// (maintainers and up).
func (c *ActionsApp) Approve(id, path *string, actorId string, number int) (*domain.ActionRun, error) {
	repo, run, _, err := c.Run(id, path, actorId, number)
	if err != nil {
		return nil, err
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, actorId, "admin")
	if err != nil {
		return nil, err
	}
	if !ok || !domain.IsPerson(actorId) {
		return nil, ErrForbidden
	}
	if run.NeedsApproval && run.Status == domain.RunQueued {
		run.NeedsApproval = false
		if err := c.Pers.UpdateRun(run, "needs_approval"); err != nil {
			return nil, err
		}
		c.Logger.Info().Str("component", "audit").Str("run_id", run.Id).Str("by", actorId).Msg("fork run approved")
	}
	return run, nil
}
