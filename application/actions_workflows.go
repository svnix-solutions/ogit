package application

import (
	"errors"
	"io"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/nektos/act/pkg/workflowpattern"
	"gopkg.in/yaml.v3"
)

// Workflow files: .ogit/workflows/*.yml|yaml if that directory has any,
// else .github/workflows. The server only reads what it needs to decide
// whether a workflow runs (its name, events and filters, and runs-on
// labels); the runner interprets the rest with act.

const (
	maxWorkflowFiles = 50
	maxWorkflowBytes = 256 << 10
)

var workflowDirs = []string{".ogit/workflows", ".github/workflows"}

// WorkflowFile is a workflow file at a commit.
type WorkflowFile struct {
	Path    string
	Content string
	Parsed  *Workflow // nil if it doesn't parse
}

// Workflow is what the server reads of a workflow file.
type Workflow struct {
	Name   string
	Events map[string]yaml.Node // event name -> its configuration (may be empty)
	Labels []string             // every job's runs-on labels
	// Permissions the run's token needs: the most any job gets (a job's
	// own `permissions:`, else the workflow's; contents: read by default).
	// Only contents and statuses are honoured.
	Permissions map[string]string
}

// honoured are the job token permissions oGit implements.
var honoured = []string{"contents", "statuses"}

// readPermissions reads a `permissions:` node (nil when absent).
func readPermissions(n yaml.Node) map[string]string {
	switch n.Kind {
	case yaml.ScalarNode:
		out := map[string]string{}
		level := map[string]string{"read-all": "read", "write-all": "write"}[n.Value]
		for _, p := range honoured {
			if level != "" {
				out[p] = level
			}
		}
		return out
	case yaml.MappingNode:
		out := map[string]string{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if slices.Contains(honoured, n.Content[i].Value) {
				out[n.Content[i].Value] = n.Content[i+1].Value
			}
		}
		return out
	}
	return nil
}

func maxLevel(a, b string) string {
	rank := map[string]int{"": 0, "none": 0, "read": 1, "write": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// PermissionString is the run's permissions as stored:
// "contents:write,statuses:write" (only what is above none).
func (w *Workflow) PermissionString() string {
	var parts []string
	for _, p := range honoured {
		if lvl := w.Permissions[p]; lvl == "read" || lvl == "write" {
			parts = append(parts, p+":"+lvl)
		}
	}
	return strings.Join(parts, ",")
}

// ReadWorkflows returns the workflow files at commit sha.
func ReadWorkflows(objects storer.EncodedObjectStorer, sha plumbing.Hash) ([]WorkflowFile, error) {
	commit, err := object.GetCommit(objects, sha)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	for _, dir := range workflowDirs {
		sub, err := tree.Tree(dir)
		if errors.Is(err, object.ErrDirectoryNotFound) || errors.Is(err, object.ErrEntryNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		var out []WorkflowFile
		for _, e := range sub.Entries {
			ext := path.Ext(e.Name)
			if (ext != ".yml" && ext != ".yaml") || (e.Mode != filemode.Regular && e.Mode != filemode.Executable) {
				continue
			}
			if len(out) == maxWorkflowFiles {
				break
			}
			blob, err := object.GetBlob(objects, e.Hash)
			if err != nil {
				return nil, err
			}
			if blob.Size > maxWorkflowBytes {
				continue
			}
			r, err := blob.Reader()
			if err != nil {
				return nil, err
			}
			data, err := io.ReadAll(r)
			r.Close()
			if err != nil {
				return nil, err
			}
			f := WorkflowFile{Path: dir + "/" + e.Name, Content: string(data)}
			f.Parsed, _ = ParseWorkflow(data)
			out = append(out, f)
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	return nil, nil
}

// ParseWorkflow reads a workflow's name, events and labels.
func ParseWorkflow(data []byte) (*Workflow, error) {
	var raw struct {
		Name        string    `yaml:"name"`
		On          yaml.Node `yaml:"on"`
		Permissions yaml.Node `yaml:"permissions"`
		Jobs        map[string]struct {
			RunsOn      yaml.Node `yaml:"runs-on"`
			Permissions yaml.Node `yaml:"permissions"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	w := &Workflow{Name: raw.Name, Events: map[string]yaml.Node{}}
	switch raw.On.Kind {
	case yaml.ScalarNode:
		w.Events[raw.On.Value] = yaml.Node{}
	case yaml.SequenceNode:
		for _, n := range raw.On.Content {
			if n.Kind == yaml.ScalarNode {
				w.Events[n.Value] = yaml.Node{}
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(raw.On.Content); i += 2 {
			w.Events[raw.On.Content[i].Value] = *raw.On.Content[i+1]
		}
	}
	if len(w.Events) == 0 || len(raw.Jobs) == 0 {
		return nil, errors.New("a workflow needs on and jobs")
	}
	defaults := readPermissions(raw.Permissions)
	if defaults == nil {
		defaults = map[string]string{"contents": "read"}
	}
	w.Permissions = map[string]string{}
	seen := map[string]bool{}
	for _, job := range raw.Jobs {
		perms := readPermissions(job.Permissions)
		if perms == nil {
			perms = defaults
		}
		for _, p := range honoured {
			w.Permissions[p] = maxLevel(w.Permissions[p], perms[p])
		}
		for _, l := range runsOn(job.RunsOn) {
			if !seen[l] && !strings.Contains(l, "${{") {
				seen[l] = true
				w.Labels = append(w.Labels, l)
			}
		}
	}
	sort.Strings(w.Labels)
	return w, nil
}

func runsOn(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode {
				out = append(out, c.Value)
			}
		}
		return out
	case yaml.MappingNode: // {group: ..., labels: ...}
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == "labels" {
				return runsOn(*n.Content[i+1])
			}
		}
	}
	return nil
}

// filters reads an event's string-list filters (branches, tags, ...).
func filters(n yaml.Node) map[string][]string {
	out := map[string][]string{}
	if n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = runsOn(*n.Content[i+1])
	}
	return out
}

// matchPatterns applies an include list and an ignore list (GitHub's
// rules, with act's pattern syntax).
func matchPatterns(name string, include, ignore []string) bool {
	if len(include) > 0 {
		p, err := workflowpattern.CompilePatterns(include...)
		if err != nil || workflowpattern.Skip(p, []string{name}, &workflowpattern.EmptyTraceWriter{}) {
			return false
		}
	}
	if len(ignore) > 0 {
		p, err := workflowpattern.CompilePatterns(ignore...)
		if err != nil || workflowpattern.Filter(p, []string{name}, &workflowpattern.EmptyTraceWriter{}) {
			return false
		}
	}
	return true
}

// MatchesPush decides whether a push to ref runs the workflow. With
// branch filters only, tags don't run it, and the other way round.
func (w *Workflow) MatchesPush(ref plumbing.ReferenceName) bool {
	cfg, ok := w.Events["push"]
	if !ok {
		return false
	}
	f := filters(cfg)
	_, hasB := f["branches"]
	_, hasBI := f["branches-ignore"]
	_, hasT := f["tags"]
	_, hasTI := f["tags-ignore"]
	branchFilters, tagFilters := hasB || hasBI, hasT || hasTI
	switch {
	case ref.IsBranch():
		if !branchFilters {
			return !tagFilters
		}
		return matchPatterns(ref.Short(), f["branches"], f["branches-ignore"])
	case ref.IsTag():
		if !tagFilters {
			return !branchFilters
		}
		return matchPatterns(ref.Short(), f["tags"], f["tags-ignore"])
	}
	return false
}
