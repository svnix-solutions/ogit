package wsagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/labbs/ogit/application"
)

// Runtime runs workspaces: DockerRuntime, or a fake in tests.
type Runtime interface {
	// Running lists the ids of running workspaces.
	Running(ctx context.Context) ([]string, error)
	// Start runs a workspace and gets its clone ready. A failed start
	// leaves no container.
	Start(ctx context.Context, task application.WorkspaceTask) error
	// Stop removes the container and keeps the volume; a missing one is fine.
	Stop(ctx context.Context, id string) error
	// Remove removes the container and the volume; missing ones are fine.
	Remove(ctx context.Context, id string) error
	// OverDisk lists the workspaces using more than their disk limit.
	OverDisk(ctx context.Context) ([]string, error)
	// Address is the host at which the agent reaches a running workspace.
	Address(ctx context.Context, id string) (string, error)
	// EditorPort is the port the editor listens on.
	EditorPort() int
}

// Agent reconciles the runtime with the server.
type Agent struct {
	Client  *Client
	Runtime Runtime
	Version string
	// MaxWorkspaces this agent runs at a time.
	MaxWorkspaces int
	// Wait is how long a sync may wait for work.
	Wait time.Duration
	// DiskEvery: how often disk use is checked (5 minutes if 0).
	DiskEvery time.Duration
	Out       io.Writer
	// ProxyURL is where the server reaches Proxy, ProxyFingerprint its
	// certificate; see ServeProxy.
	ProxyURL         string
	ProxyFingerprint string

	mu      sync.Mutex
	key     string // the server's, from the last sync
	busy    map[string]bool
	failed  map[string]string
	removed map[string]bool
	wg      sync.WaitGroup
}

func (a *Agent) logf(format string, args ...any) {
	if a.Out != nil {
		fmt.Fprintf(a.Out, format+"\n", args...)
	}
}

// failure never carries the token, whatever the failing command printed.
func failure(err error, token string) string {
	message := err.Error()
	if token != "" {
		message = strings.ReplaceAll(message, token, "[token]")
	}
	return message
}

// Run reconciles until ctx ends, and fails when the server no longer knows
// the agent. Containers keep running across restarts of the agent.
func (a *Agent) Run(ctx context.Context) error {
	defer a.wg.Wait()
	every := a.DiskEvery
	if every <= 0 {
		every = 5 * time.Minute
	}
	nextDisk := time.Now().Add(every)
	backoff := time.Second
	for ctx.Err() == nil {
		if !time.Now().Before(nextDisk) {
			nextDisk = time.Now().Add(every)
			a.checkDisk(ctx)
		}
		err := a.Sync(ctx)
		if errors.Is(err, ErrUnregistered) {
			return err
		}
		if err == nil {
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			break
		}
		a.logf("sync failed: %v (retrying in %s)", err, backoff)
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
	return nil
}

func (a *Agent) checkDisk(ctx context.Context) {
	over, err := a.Runtime.OverDisk(ctx)
	if err != nil {
		a.logf("checking disk use failed: %v", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failed == nil {
		a.failed = map[string]string{}
	}
	for _, id := range over {
		a.failed[id] = "over its disk limit"
	}
}

// Sync does one round: report, then start what the server answered, each
// task on its own so that a long image pull doesn't stop the reports.
func (a *Agent) Sync(ctx context.Context) error {
	running, err := a.Runtime.Running(ctx)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.busy == nil {
		a.busy, a.failed, a.removed = map[string]bool{}, map[string]string{}, map[string]bool{}
	}
	report := application.AgentReport{Version: a.Version, Running: running, Busy: slices.Collect(maps.Keys(a.busy)),
		Removed: slices.Collect(maps.Keys(a.removed)), Wait: int(a.Wait.Seconds())}
	report.Slots = max(a.MaxWorkspaces-len(running)-len(a.busy), 0)
	report.ProxyURL, report.ProxyFingerprint = a.ProxyURL, a.ProxyFingerprint
	failed := maps.Clone(a.failed)
	for id, message := range failed {
		report.Failed = append(report.Failed, application.AgentFailure{Id: id, Message: message})
	}
	removed := slices.Clone(report.Removed)
	if len(report.Failed)+len(report.Removed) > 0 || a.key == "" {
		report.Wait = 0
	}
	a.mu.Unlock()

	tasks, err := a.Client.Sync(ctx, report)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.key = tasks.ProxyKey
	for id := range failed {
		delete(a.failed, id)
	}
	for _, id := range removed {
		delete(a.removed, id)
	}
	for _, task := range tasks.Start {
		a.spawn(task.Id, func() {
			if err := a.Runtime.Start(ctx, task); err != nil {
				a.mu.Lock()
				a.failed[task.Id] = failure(err, task.Token)
				a.mu.Unlock()
				a.logf("workspace %s failed to start", task.Id)
			}
		})
	}
	for _, id := range tasks.Stop {
		a.spawn(id, func() {
			if err := a.Runtime.Stop(ctx, id); err != nil {
				a.logf("stopping workspace %s failed: %v", id, err)
			}
		})
	}
	for _, id := range tasks.Remove {
		a.spawn(id, func() {
			if err := a.Runtime.Remove(ctx, id); err != nil {
				a.logf("removing workspace %s failed: %v", id, err)
				return
			}
			a.mu.Lock()
			a.removed[id] = true
			a.mu.Unlock()
		})
	}
	return nil
}

// spawn runs work unless the workspace is busy; called with mu held.
func (a *Agent) spawn(id string, work func()) {
	if a.busy[id] {
		return
	}
	a.busy[id] = true
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		work()
		a.mu.Lock()
		delete(a.busy, id)
		a.mu.Unlock()
	}()
}

// Settle waits for tasks in progress, for tests.
func (a *Agent) Settle() { a.wg.Wait() }
