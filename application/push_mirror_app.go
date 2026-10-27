package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/labbs/ogit/infrastructure/mirror"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

// Push mirror rules:
//   - owners and maintainers add push mirrors to a repository (at most
//     maxPushMirrors): a remote like a pull mirror's (https:// or http://
//     with an optional user name and password or token, or SSH with a
//     generated key whose public key goes on the remote with write access;
//     secrets sealed, netguard, the SSH host key pinned on first contact),
//     a mode and an interval; they see them (never the secrets), change
//     them, sync them now and delete them;
//   - mode "mirror" makes the remote's branches and tags identical (forced
//     updates, the ones we don't have deleted; other refs untouched);
//     "branches" never forces or deletes: diverged branches and moved tags
//     are reported and the other refs still pushed;
//   - every push to the repository (git, merges, release tags, fork syncs,
//     pull mirror syncs: WebhookApp.Pushed) makes its push mirrors due, and
//     the worker pushes within seconds, never holding up the push; a push
//     during a sync makes the mirror due again (generation); mirrors also
//     sync every interval;
//   - a repository without branches pushes nothing (never wiping the
//     remote);
//   - with LFS, the objects the pushed tips point to are uploaded to the
//     remote's LFS server first;
//   - failures (rejected refs included) are kept and retried with backoff
//     (5 minutes doubling, up to the interval).

var (
	ErrPushMirrorNotFound = errors.New("push mirror not found")
	ErrTooManyPushMirrors = errors.New("a repository has at most 10 push mirrors")
)

const maxPushMirrors = 10

type PushMirrorApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.PushMirrorPers
	Repositories *RepositoryApp
	Storage      storage.Config
	LFS          *LFSApp
	Box          *secretbox.Box // nil without security.encryption_key
	Dialer       mirror.Dialer
	Now          func() time.Time

	once sync.Once
	wake chan struct{}
}

func (c *PushMirrorApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

func (c *PushMirrorApp) wakeup() chan struct{} {
	c.once.Do(func() { c.wake = make(chan struct{}, 1) })
	return c.wake
}

// PushMirrorInput describes a new push mirror or a change.
type PushMirrorInput struct {
	MirrorInput
	Mode *string
}

// managed returns a repository if actorId maintains it.
func (c *PushMirrorApp) managed(id, path *string, actorId string) (*domain.Repository, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if role.Level() < domain.AccessTypeMaintainer.Level() {
		return nil, ErrForbidden
	}
	return repo, nil
}

func (c *PushMirrorApp) get(id, path *string, mirrorId, actorId string) (*domain.Repository, *domain.PushMirror, error) {
	repo, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, nil, err
	}
	m, err := c.Pers.Get(repo.Id, mirrorId)
	if isNotFound(err) {
		return nil, nil, ErrPushMirrorNotFound
	}
	return repo, m, err
}

func (c *PushMirrorApp) apply(m *domain.PushMirror, in PushMirrorInput) error {
	if err := applyRemote(c.Box, &m.MirrorRemote, in.MirrorInput); err != nil {
		return err
	}
	if in.Mode != nil {
		if *in.Mode != domain.PushMirrorForce && *in.Mode != domain.PushMirrorBranches {
			return ErrInvalidMirror
		}
		m.Mode = *in.Mode
	}
	if in.LFS != nil {
		m.LFS = *in.LFS
	}
	if in.Interval != nil {
		secs, err := mirrorInterval(c.Config, *in.Interval)
		if err != nil {
			return err
		}
		m.IntervalSeconds = secs
	}
	return nil
}

// List returns a repository's push mirrors.
func (c *PushMirrorApp) List(id, path *string, actorId string) ([]domain.PushMirror, error) {
	repo, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, err
	}
	return c.Pers.List(repo.Id)
}

// Create adds a push mirror; its first push starts at once.
func (c *PushMirrorApp) Create(id, path *string, actorId string, in PushMirrorInput) (*domain.PushMirror, error) {
	repo, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if in.URL == nil {
		return nil, ErrInvalidMirror
	}
	existing, err := c.Pers.List(repo.Id)
	if err != nil {
		return nil, err
	}
	if len(existing) >= maxPushMirrors {
		return nil, ErrTooManyPushMirrors
	}
	interval := c.Config.Mirrors.DefaultInterval
	if interval <= 0 {
		interval = 8 * time.Hour
	}
	now := c.now()
	m := &domain.PushMirror{RepositoryId: repo.Id, MirrorRemote: domain.MirrorRemote{Auth: domain.MirrorAuthNone}, Mode: domain.PushMirrorForce,
		IntervalSeconds: int(interval.Seconds()), Status: domain.MirrorPending, NextSyncAt: now, CreatedAt: now, UpdatedAt: now}
	if err := c.apply(m, in); err != nil {
		return nil, err
	}
	if err := c.Pers.Create(m); err != nil {
		return nil, err
	}
	c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", "push_mirror.create").Str("repository", repo.FullPath()).
		Str("url", mirror.SafeURL(m.URL)).Str("mode", m.Mode).Msg("push mirror added")
	c.poke()
	return m, nil
}

// Update changes a push mirror; a new URL, credentials or mode sync at
// once.
func (c *PushMirrorApp) Update(id, path *string, mirrorId, actorId string, in PushMirrorInput) (*domain.PushMirror, error) {
	_, m, err := c.get(id, path, mirrorId, actorId)
	if err != nil {
		return nil, err
	}
	if err := c.apply(m, in); err != nil {
		return nil, err
	}
	fields := map[string]any{"url": m.URL, "auth": m.Auth, "username": m.Username, "secret": m.Secret, "ssh_public_key": m.SSHPublicKey,
		"ssh_host_key": m.SSHHostKey, "mode": m.Mode, "lfs": m.LFS, "interval_seconds": m.IntervalSeconds, "updated_at": c.now()}
	if in.URL != nil || in.Username != nil || in.Password != nil || in.SSH || in.Mode != nil || in.LFS != nil {
		m.NextSyncAt, m.Generation = c.now(), m.Generation+1 // a sync running now won't reschedule it
		fields["next_sync_at"], fields["generation"] = m.NextSyncAt, m.Generation
	}
	if err := c.Pers.Update(m.Id, fields); err != nil {
		return nil, err
	}
	c.poke()
	return m, nil
}

// SyncNow makes a push mirror due.
func (c *PushMirrorApp) SyncNow(id, path *string, mirrorId, actorId string) (*domain.PushMirror, error) {
	_, m, err := c.get(id, path, mirrorId, actorId)
	if err != nil {
		return nil, err
	}
	if m.LockedUntil != nil && m.LockedUntil.After(c.now()) {
		return nil, ErrMirrorInProgress
	}
	m.NextSyncAt = c.now()
	if err := c.Pers.Update(m.Id, map[string]any{"next_sync_at": m.NextSyncAt}); err != nil {
		return nil, err
	}
	c.poke()
	return m, nil
}

// Delete removes a push mirror (the remote keeps what it got).
func (c *PushMirrorApp) Delete(id, path *string, mirrorId, actorId string) error {
	repo, m, err := c.get(id, path, mirrorId, actorId)
	if err != nil {
		return err
	}
	if err := c.Pers.Delete(m.Id); err != nil {
		return err
	}
	c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", "push_mirror.delete").Str("repository", repo.FullPath()).
		Str("url", mirror.SafeURL(m.URL)).Msg("push mirror removed")
	return nil
}

// Pushed makes a repository's push mirrors due (after any push).
func (c *PushMirrorApp) Pushed(repo *domain.Repository) {
	if c == nil {
		return
	}
	if err := c.Pers.Touch(repo.Id, c.now()); err != nil {
		c.Logger.Error().Err(err).Str("component", "application.push_mirror").Str("repository", repo.FullPath()).Msg("failed to queue push mirrors")
		return
	}
	c.poke()
}

// poke wakes this server's workers.
func (c *PushMirrorApp) poke() {
	select {
	case c.wakeup() <- struct{}{}:
	default:
	}
}

// Run syncs due push mirrors until ctx ends, with a few workers, checking
// every interval and whenever a push makes one due here.
func (c *PushMirrorApp) Run(ctx context.Context, every time.Duration, workers int) {
	var wg sync.WaitGroup
	for range max(workers, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				case <-c.wakeup():
				}
				for c.SyncDue(ctx) > 0 {
				}
			}
		}()
	}
	wg.Wait()
}

// SyncDue pushes one due mirror, if any, and returns how many it pushed.
func (c *PushMirrorApp) SyncDue(ctx context.Context) int {
	m, err := c.Pers.Claim(c.now(), c.now().Add(mirrorLock))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.push_mirror").Msg("failed to claim a push mirror")
		return 0
	}
	if m == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, mirrorLock-time.Minute)
	defer cancel()
	c.sync(ctx, m)
	return 1
}

func (c *PushMirrorApp) sync(ctx context.Context, m *domain.PushMirror) {
	now := c.now()
	repo, err := c.Repositories.GetRepository(&m.RepositoryId, nil)
	if err != nil {
		_ = c.Pers.Delete(m.Id) // the repository is gone
		return
	}
	hostKey := m.SSHHostKey
	err = c.push(ctx, repo, m, &hostKey)
	fields := map[string]any{"locked_until": nil, "last_sync_at": now, "updated_at": now, "ssh_host_key": hostKey}
	if err != nil {
		m.Failures++
		fields["status"], fields["failures"], fields["last_error"], fields["next_sync_at"] = domain.MirrorFailed, m.Failures, mirror.Describe(err), now.Add(mirrorRetry(m.Failures, m.IntervalSeconds))
		c.Logger.Warn().Err(err).Str("component", "application.push_mirror").Str("repository", repo.FullPath()).Str("url", mirror.SafeURL(m.URL)).Msg("push mirror sync failed")
		_ = c.Pers.Update(m.Id, fields)
		return
	}
	fields["status"], fields["failures"], fields["last_error"], fields["last_success_at"] = domain.MirrorOK, 0, "", now
	if err := c.Pers.Update(m.Id, fields); err != nil {
		return
	}
	// Unless pushed meanwhile (then it stays due).
	_ = c.Pers.Schedule(m.Id, m.Generation, now.Add(time.Duration(m.IntervalSeconds)*time.Second))
}

func (c *PushMirrorApp) push(ctx context.Context, repo *domain.Repository, m *domain.PushMirror, hostKey *string) error {
	cred, err := remoteCredentials(c.Box, &m.MirrorRemote, hostKey)
	if err != nil {
		return err
	}
	remote, err := c.Dialer.Remote(ctx, m.URL, cred)
	if err != nil {
		return err
	}
	st, err := c.Storage.GetStorer(repo.StorageKey)
	if err != nil {
		return err
	}
	opts := mirror.PushOptions{Force: m.Mode == domain.PushMirrorForce}
	if m.LFS {
		opts.BeforeSend = func(tips []plumbing.Hash) error {
			objects, err := mirror.Pointers(st, tips)
			if err != nil || len(objects) == 0 {
				return err
			}
			server, err := c.Dialer.LFSServer(ctx, m.URL, cred, "upload")
			if err != nil {
				return err
			}
			acc := &LFSAccess{Repo: repo}
			return c.Dialer.PushLFS(ctx, server, objects, func(o mirror.LFSObject) (io.ReadCloser, error) {
				r, _, err := c.LFS.Download(acc, o.Oid)
				if errors.Is(err, ErrLFSNotFound) {
					return nil, nil // not here either: the pointer stays a pointer
				}
				return r, err
			})
		}
	}
	res, err := mirror.Push(ctx, st, remote, opts)
	if err != nil {
		return err
	}
	if len(res.Updated) > 0 {
		c.Logger.Info().Str("component", "application.push_mirror").Str("repository", repo.FullPath()).Str("url", mirror.SafeURL(m.URL)).
			Str("refs", strings.Join(res.Updated, ",")).Msg("pushed to mirror")
	}
	return res.Err()
}
