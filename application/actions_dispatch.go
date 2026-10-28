package application

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

// Manual and scheduled runs:
//   - workflow_dispatch: writers run a workflow of a branch (the default
//     one if none) that has `on: workflow_dispatch`, with its inputs
//     (declared ones only; required ones need a value or a default);
//   - schedule: the `cron:` entries of the default branch's workflows,
//     refreshed on each push to it, at most every 5 minutes (more frequent
//     ones are ignored); each due schedule is claimed once (conditional
//     UPDATE), so several servers run it once.

var (
	ErrWorkflowNotFound = errors.New("workflow not found, or it has no workflow_dispatch trigger")
	ErrInvalidInputs    = errors.New("invalid workflow inputs")
)

const minScheduleInterval = 5 * time.Minute

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// schedulesOf reads a workflow's valid crons.
func schedulesOf(w *Workflow) []string {
	cfg, ok := w.Events["schedule"]
	if !ok || cfg.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, entry := range cfg.Content {
		spec := filters(*entry)["cron"]
		if len(spec) != 1 {
			continue
		}
		sched, err := cronParser.Parse(spec[0])
		if err != nil {
			continue
		}
		// Refuse crons that fire more often than every 5 minutes.
		first := sched.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		ok := true
		for t, i := first, 0; i < 300; i++ {
			next := sched.Next(t)
			if next.Sub(t) < minScheduleInterval {
				ok = false
				break
			}
			t = next
		}
		if ok {
			out = append(out, spec[0])
		}
	}
	return out
}

// updateSchedules replaces a repository's schedules with its default
// branch's.
func (c *ActionsApp) updateSchedules(repo *domain.Repository, files []WorkflowFile) {
	var schedules []domain.ActionSchedule
	now := c.now().UTC()
	for _, f := range files {
		if f.Parsed == nil {
			continue
		}
		for _, spec := range schedulesOf(f.Parsed) {
			sched, _ := cronParser.Parse(spec)
			schedules = append(schedules, domain.ActionSchedule{WorkflowPath: f.Path, Cron: spec, NextRunAt: sched.Next(now).UTC()})
		}
	}
	if err := c.Pers.ReplaceSchedules(repo.Id, schedules); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.actions").Msg("failed to update schedules")
	}
}

// RunSchedules queues the runs of due schedules; it returns how many.
func (c *ActionsApp) RunSchedules() int {
	now := c.now().UTC() // compared as text by SQLite
	due, err := c.Pers.DueSchedules(now)
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range due {
		sched, err := cronParser.Parse(s.Cron)
		if err != nil {
			continue
		}
		ok, err := c.Pers.ClaimSchedule(s.Id, s.NextRunAt, sched.Next(now).UTC())
		if err != nil || !ok {
			continue // another server took it
		}
		repo, err := c.Repositories.GetRepository(&s.RepositoryId, nil)
		if err != nil {
			continue
		}
		f, tip, err := c.workflowAt(repo, plumbing.NewBranchReferenceName(repo.DefaultBranch), s.WorkflowPath)
		if err != nil {
			continue
		}
		payload, _ := json.Marshal(map[string]any{"schedule": s.Cron, "repository": c.Webhooks.repositoryPayload(repo)})
		run := c.newRun(repo, *f, "schedule", string(payload), plumbing.NewBranchReferenceName(repo.DefaultBranch).String(), tip, "", "")
		if err := c.Pers.CreateRun(run); err == nil {
			n++
		}
	}
	return n
}

// workflowAt finds a workflow file (by path or file name) at ref's tip.
func (c *ActionsApp) workflowAt(repo *domain.Repository, ref plumbing.ReferenceName, file string) (*WorkflowFile, plumbing.Hash, error) {
	st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey)
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	tip, err := st.Reference(ref)
	if err != nil {
		return nil, plumbing.ZeroHash, ErrRefNotFoundForDispatch
	}
	files, err := ReadWorkflows(st, tip.Hash())
	if err != nil {
		return nil, plumbing.ZeroHash, err
	}
	for i := range files {
		if (files[i].Path == file || path.Base(files[i].Path) == file) && files[i].Parsed != nil {
			return &files[i], tip.Hash(), nil
		}
	}
	return nil, plumbing.ZeroHash, ErrWorkflowNotFound
}

var ErrRefNotFoundForDispatch = errors.New("branch not found")

// dispatchInputs checks inputs against the workflow's declared ones and
// fills in defaults.
func dispatchInputs(cfg yaml.Node, given map[string]string) (map[string]string, error) {
	declared := map[string]yaml.Node{}
	for k, v := range filtersNodes(cfg)["inputs"] {
		declared[k] = v
	}
	out := map[string]string{}
	for name := range given {
		if _, ok := declared[name]; !ok {
			return nil, fmt.Errorf("%w: %s isn't one of the workflow's inputs", ErrInvalidInputs, name)
		}
	}
	for name, def := range declared {
		props := map[string]string{}
		if def.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(def.Content); i += 2 {
				props[def.Content[i].Value] = def.Content[i+1].Value
			}
		}
		v, ok := given[name]
		if !ok {
			v, ok = props["default"]
		}
		if !ok && props["required"] == "true" {
			return nil, fmt.Errorf("%w: %s is required", ErrInvalidInputs, name)
		}
		if ok {
			out[name] = v
		}
	}
	return out, nil
}

// filtersNodes returns an event configuration's mapping entries, each a
// mapping of its own (inputs: name -> definition).
func filtersNodes(n yaml.Node) map[string]map[string]yaml.Node {
	out := map[string]map[string]yaml.Node{}
	if n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		inner := map[string]yaml.Node{}
		v := n.Content[i+1]
		if v.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(v.Content); j += 2 {
				inner[v.Content[j].Value] = *v.Content[j+1]
			}
		}
		out[n.Content[i].Value] = inner
	}
	return out
}

// Dispatch runs a workflow by hand (writers).
func (c *ActionsApp) Dispatch(id, idPath *string, actorId, workflow, ref string, inputs map[string]string) (*domain.ActionRun, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, idPath, actorId)
	if err != nil {
		return nil, err
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, actorId, "write")
	if err != nil {
		return nil, err
	}
	if !ok || !domain.IsPerson(actorId) {
		return nil, ErrForbidden
	}
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	if ref == "" {
		ref = repo.DefaultBranch
	}
	refName := plumbing.NewBranchReferenceName(ref)
	f, tip, err := c.workflowAt(repo, refName, workflow)
	if err != nil {
		return nil, err
	}
	cfg, ok := f.Parsed.Events["workflow_dispatch"]
	if !ok {
		return nil, ErrWorkflowNotFound
	}
	values, err := dispatchInputs(cfg, inputs)
	if err != nil {
		return nil, err
	}
	actor, _ := c.Repositories.UserPers.GetById(actorId)
	payload, _ := json.Marshal(map[string]any{
		"inputs": values, "ref": refName.String(), "workflow": f.Path,
		"repository": c.Webhooks.repositoryPayload(repo), "sender": c.Webhooks.sender(actorId, ""),
	})
	run := c.newRun(repo, *f, "workflow_dispatch", string(payload), refName.String(), tip, actorId, actor.Username)
	if err := c.Pers.CreateRun(run); err != nil {
		return nil, err
	}
	return run, nil
}

// WorkflowInput is a workflow_dispatch input, for the "Run workflow" form.
type WorkflowInput struct {
	Name        string
	Description string
	Required    bool
	Default     string
	Type        string // string (default), boolean, choice, number, environment
	Options     []string
}

// WorkflowInfo describes a workflow of the default branch.
type WorkflowInfo struct {
	Path     string
	Name     string
	Dispatch bool
	Inputs   []WorkflowInput
	Error    string // set when the file doesn't parse
}

// Workflows lists the default branch's workflows (any reader), and whether
// the repository uses Actions (workflows or runs).
func (c *ActionsApp) Workflows(id, idPath *string, readerId string) ([]WorkflowInfo, bool, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, idPath, readerId)
	if err != nil {
		return nil, false, err
	}
	out := []WorkflowInfo{}
	if st, err := c.Repositories.StorageConfig.GetStorer(repo.StorageKey); err == nil {
		if tip, err := st.Reference(plumbing.NewBranchReferenceName(repo.DefaultBranch)); err == nil {
			files, err := ReadWorkflows(st, tip.Hash())
			if err != nil {
				return nil, false, err
			}
			for _, f := range files {
				info := WorkflowInfo{Path: f.Path, Name: f.Path}
				if f.Parsed == nil {
					info.Error = "this workflow file doesn't parse"
				} else {
					if f.Parsed.Name != "" {
						info.Name = f.Parsed.Name
					}
					if cfg, ok := f.Parsed.Events["workflow_dispatch"]; ok {
						info.Dispatch, info.Inputs = true, inputsOf(cfg)
					}
				}
				out = append(out, info)
			}
		}
	}
	used := len(out) > 0
	if !used {
		_, total, err := c.Pers.ListRuns(repo.Id, domain.RunFilter{}, 0, 1)
		if err != nil {
			return nil, false, err
		}
		used = total > 0
	}
	return out, used, nil
}

func inputsOf(cfg yaml.Node) []WorkflowInput {
	var out []WorkflowInput
	if cfg.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(cfg.Content); i += 2 {
		if cfg.Content[i].Value != "inputs" || cfg.Content[i+1].Kind != yaml.MappingNode {
			continue
		}
		inputs := cfg.Content[i+1]
		for j := 0; j+1 < len(inputs.Content); j += 2 {
			in := WorkflowInput{Name: inputs.Content[j].Value, Type: "string"}
			def := inputs.Content[j+1]
			for k := 0; def.Kind == yaml.MappingNode && k+1 < len(def.Content); k += 2 {
				key, val := def.Content[k].Value, def.Content[k+1]
				switch key {
				case "description":
					in.Description = val.Value
				case "required":
					in.Required = val.Value == "true"
				case "default":
					in.Default = val.Value
				case "type":
					in.Type = val.Value
				case "options":
					in.Options = runsOn(*val)
				}
			}
			out = append(out, in)
		}
	}
	return out
}
