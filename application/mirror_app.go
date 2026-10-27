package application

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/helpers/secretbox"
	"github.com/labbs/ogit/infrastructure/mirror"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

// Pull mirror and import rules:
//   - importing creates a repository where the actor may create one and
//     fetches a remote's branches and tags into it: once (an import: a
//     normal repository afterwards, its mirror row goes) or on a schedule
//     (a mirror: read-only, its branches and tags follow the remote, ones
//     deleted there go here, pushes and merges refused until mirroring
//     stops; other refs, refs/pull/..., are kept);
//   - remotes are https:// or http:// (an optional user name and password
//     or token) or ssh:// and git@host:path (a key pair generated for the
//     mirror, whose public key the user adds on the remote); secrets need
//     security.encryption_key and are sealed; the remote's address goes
//     through netguard (mirrors.allowed_hosts) and an SSH host key is
//     pinned on first contact;
//   - owners and maintainers see a mirror (URL, status, last error, SSH
//     public key; never the secrets), change it and sync it now; owners
//     stop mirroring (the repository becomes a normal one);
//   - syncs run every interval (mirrors.default_interval, at least
//     mirrors.min_interval), each claimed by one server; failures are kept
//     and retried with backoff (5 minutes doubling, up to the interval);
//   - with LFS, the objects the fetched branch and tag tips point to are
//     fetched from the remote's LFS server too;
//   - syncs send no webhooks and start no Actions runs (a mirror is a
//     copy).

var (
	ErrMirrorNotFound   = errors.New("this repository is not a mirror")
	ErrInvalidMirror    = errors.New("invalid mirror: check the URL (https://, http://, ssh:// or git@host:path, no credentials in it) and the interval")
	ErrMirrorNeedsKey   = errors.New("credentials and SSH keys need security.encryption_key on the server")
	ErrMirrorInProgress = errors.New("a sync is already running")
)

const (
	mirrorLock    = 30 * time.Minute // a sync's longest run
	mirrorBackoff = 5 * time.Minute
)

type MirrorApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.MirrorPers
	Repositories *RepositoryApp
	Storage      storage.Config
	LFS          *LFSApp
	Box          *secretbox.Box // nil without security.encryption_key
	Dialer       mirror.Dialer
	Now          func() time.Time
}

func (c *MirrorApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

func (c *MirrorApp) defaultInterval() time.Duration {
	if c.Config.Mirrors.DefaultInterval > 0 {
		return c.Config.Mirrors.DefaultInterval
	}
	return 8 * time.Hour
}

func (c *MirrorApp) minInterval() time.Duration {
	if c.Config.Mirrors.MinInterval > 0 {
		return c.Config.Mirrors.MinInterval
	}
	return 10 * time.Minute
}

// MirrorInput describes an import or a mirror's change.
type MirrorInput struct {
	URL      *string
	Username *string
	Password *string // write-only; "" removes it
	SSH      bool    // generate a key pair (imports over SSH)
	LFS      *bool
	Interval *time.Duration
}

// ImportInput: a new repository from a remote.
type ImportInput struct {
	Namespace, Name, Description string
	Private                      bool
	Mirror                       bool // keep syncing (read-only)
	MirrorInput
}

func (c *MirrorApp) seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	if c.Box == nil {
		return "", ErrMirrorNeedsKey
	}
	return c.Box.Seal(plain)
}

// Import creates a repository from a remote; the first sync starts at
// once (in the background).
func (c *MirrorApp) Import(actorId string, in ImportInput) (*domain.Repository, *domain.Mirror, error) {
	if in.URL == nil || mirror.ValidateURL(*in.URL) != nil {
		return nil, nil, ErrInvalidMirror
	}
	m := &domain.Mirror{URL: strings.TrimSpace(*in.URL), Mirror: in.Mirror, Auth: domain.MirrorAuthNone,
		IntervalSeconds: int(c.defaultInterval().Seconds()), Status: domain.MirrorPending, NextSyncAt: c.now(), CreatedAt: c.now(), UpdatedAt: c.now()}
	if err := c.apply(m, in.MirrorInput, true); err != nil {
		return nil, nil, err
	}
	tmpl := &domain.Repository{Name: in.Name, Description: in.Description, IsPrivate: in.Private, DefaultBranch: "main", HasIssues: true, IsMirror: in.Mirror}
	repo, err := c.Repositories.create(in.Namespace, actorId, tmpl, func(r *domain.Repository) error {
		return c.Storage.InitRepository(r.StorageKey)
	})
	if err != nil {
		return nil, nil, err
	}
	m.RepositoryId = repo.Id
	if err := c.Pers.Create(m); err != nil {
		return nil, nil, err
	}
	c.Logger.Info().Str("component", "application.mirror").Str("repository", repo.FullPath()).Str("url", mirror.SafeURL(m.URL)).Bool("mirror", m.Mirror).Msg("import queued")
	return repo, m, nil
}

// apply copies in into m (fresh: a new mirror).
func (c *MirrorApp) apply(m *domain.Mirror, in MirrorInput, fresh bool) error {
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		if mirror.ValidateURL(u) != nil {
			return ErrInvalidMirror
		}
		if u != m.URL {
			m.SSHHostKey = "" // another host: pin its key anew
		}
		m.URL = u
	}
	if mirror.IsSSH(m.URL) {
		if m.Auth != domain.MirrorAuthSSH || in.SSH {
			if c.Box == nil {
				return ErrMirrorNeedsKey
			}
			priv, pub, err := mirror.GenerateSSHKey("ogit-mirror")
			if err != nil {
				return err
			}
			if m.Secret, err = c.seal(string(priv)); err != nil {
				return err
			}
			m.Auth, m.SSHPublicKey, m.Username = domain.MirrorAuthSSH, pub, ""
		}
	} else {
		if m.Auth == domain.MirrorAuthSSH {
			m.Auth, m.Secret, m.SSHPublicKey, m.SSHHostKey = domain.MirrorAuthNone, "", "", ""
		}
		if in.Username != nil {
			m.Username = strings.TrimSpace(*in.Username)
		}
		if in.Password != nil {
			sealed, err := c.seal(*in.Password)
			if err != nil {
				return err
			}
			m.Secret = sealed
		}
		m.Auth = domain.MirrorAuthNone
		if m.Secret != "" || m.Username != "" {
			m.Auth = domain.MirrorAuthPassword
		}
	}
	if in.LFS != nil {
		m.LFS = *in.LFS
	}
	if in.Interval != nil {
		if *in.Interval < c.minInterval() || *in.Interval > 30*24*time.Hour {
			return ErrInvalidMirror
		}
		m.IntervalSeconds = int(in.Interval.Seconds())
	}
	return nil
}

// managed returns a repository's mirror if actorId maintains it.
func (c *MirrorApp) managed(id, path *string, actorId string) (*domain.Repository, *domain.Mirror, domain.AccessType, error) {
	repo, role, err := c.Repositories.repositoryForManager(id, path, actorId)
	if err != nil {
		return nil, nil, role, err
	}
	if role.Level() < domain.AccessTypeMaintainer.Level() {
		return nil, nil, role, ErrForbidden
	}
	m, err := c.Pers.ForRepository(repo.Id)
	if isNotFound(err) {
		return nil, nil, role, ErrMirrorNotFound
	}
	return repo, m, role, err
}

// Get returns a repository's mirror (maintainers and owners).
func (c *MirrorApp) Get(id, path *string, actorId string) (*domain.Repository, *domain.Mirror, error) {
	repo, m, _, err := c.managed(id, path, actorId)
	return repo, m, err
}

// MirrorOf returns a repository's mirror for display to its readers (URL
// and status only are shown), or nil.
func (c *MirrorApp) MirrorOf(repo *domain.Repository) *domain.Mirror {
	m, err := c.Pers.ForRepository(repo.Id)
	if err != nil {
		return nil
	}
	return m
}

// Update changes a mirror; a new URL or credentials sync at once.
func (c *MirrorApp) Update(id, path *string, actorId string, in MirrorInput) (*domain.Mirror, error) {
	_, m, _, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if err := c.apply(m, in, false); err != nil {
		return nil, err
	}
	fields := map[string]any{"url": m.URL, "auth": m.Auth, "username": m.Username, "secret": m.Secret, "ssh_public_key": m.SSHPublicKey,
		"ssh_host_key": m.SSHHostKey, "lfs": m.LFS, "interval_seconds": m.IntervalSeconds, "updated_at": c.now()}
	if in.URL != nil || in.Username != nil || in.Password != nil || in.SSH {
		m.NextSyncAt = c.now()
		fields["next_sync_at"] = m.NextSyncAt
	}
	return m, c.Pers.Update(m.Id, fields)
}

// SyncNow makes the mirror due (the worker picks it up within seconds).
func (c *MirrorApp) SyncNow(id, path *string, actorId string) (*domain.Mirror, error) {
	_, m, _, err := c.managed(id, path, actorId)
	if err != nil {
		return nil, err
	}
	if m.LockedUntil != nil && m.LockedUntil.After(c.now()) {
		return nil, ErrMirrorInProgress
	}
	m.NextSyncAt = c.now()
	return m, c.Pers.Update(m.Id, map[string]any{"next_sync_at": m.NextSyncAt})
}

// Stop ends mirroring (owners): the repository becomes a normal one.
func (c *MirrorApp) Stop(id, path *string, actorId string) error {
	repo, m, role, err := c.managed(id, path, actorId)
	if err != nil {
		return err
	}
	if role != domain.AccessTypeOwner {
		return ErrForbidden
	}
	if err := c.Pers.Delete(m.Id); err != nil {
		return err
	}
	if repo.IsMirror {
		if err := c.Repositories.RepoPres.Update(repo.Id, map[string]any{"is_mirror": false}); err != nil {
			return err
		}
	}
	c.Logger.Info().Str("component", "audit").Str("actor_id", actorId).Str("action", "mirror.stop").Str("repository", repo.FullPath()).Msg("mirroring stopped")
	return nil
}

// Run syncs due mirrors until ctx ends, checking every interval.
func (c *MirrorApp) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for c.SyncDue(ctx) > 0 {
			}
		}
	}
}

// SyncDue syncs one due mirror, if any, and returns how many it synced.
// The worker calls it in a loop; tests call it directly.
func (c *MirrorApp) SyncDue(ctx context.Context) int {
	m, err := c.Pers.Claim(c.now(), c.now().Add(mirrorLock))
	if err != nil {
		c.Logger.Error().Err(err).Str("component", "application.mirror").Msg("failed to claim a mirror")
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

// sync fetches the remote and records the outcome.
func (c *MirrorApp) sync(ctx context.Context, m *domain.Mirror) {
	now := c.now()
	repo, err := c.Repositories.GetRepository(&m.RepositoryId, nil)
	if err != nil {
		_ = c.Pers.Delete(m.Id) // the repository is gone
		return
	}
	hostKey := m.SSHHostKey
	err = c.fetch(ctx, repo, m, &hostKey)
	fields := map[string]any{"locked_until": nil, "last_sync_at": now, "updated_at": now, "ssh_host_key": hostKey}
	if err != nil {
		m.Failures++
		wait := mirrorBackoff << min(m.Failures-1, 10)
		if interval := time.Duration(m.IntervalSeconds) * time.Second; wait > interval {
			wait = interval
		}
		fields["status"], fields["failures"], fields["last_error"], fields["next_sync_at"] = domain.MirrorFailed, m.Failures, mirror.Describe(err), now.Add(wait)
		c.Logger.Warn().Err(err).Str("component", "application.mirror").Str("repository", repo.FullPath()).Msg("mirror sync failed")
		_ = c.Pers.Update(m.Id, fields)
		return
	}
	if !m.Mirror {
		_ = c.Pers.Delete(m.Id) // imported: a normal repository now
		c.Logger.Info().Str("component", "application.mirror").Str("repository", repo.FullPath()).Msg("import done")
		return
	}
	fields["status"], fields["failures"], fields["last_error"], fields["last_success_at"] = domain.MirrorOK, 0, "", now
	fields["next_sync_at"] = now.Add(time.Duration(m.IntervalSeconds) * time.Second)
	_ = c.Pers.Update(m.Id, fields)
}

func (c *MirrorApp) credentials(m *domain.Mirror, hostKey *string) (mirror.Credentials, error) {
	cred := mirror.Credentials{Username: m.Username, SSHHostKey: *hostKey, OnHostKey: func(k string) { *hostKey = k }}
	if m.Secret != "" {
		if c.Box == nil {
			return cred, ErrMirrorNeedsKey
		}
		plain, err := c.Box.Open(m.Secret)
		if err != nil {
			return cred, err
		}
		if m.Auth == domain.MirrorAuthSSH {
			cred.SSHPrivateKey = []byte(plain)
		} else {
			cred.Password = plain
		}
	}
	return cred, nil
}

func (c *MirrorApp) fetch(ctx context.Context, repo *domain.Repository, m *domain.Mirror, hostKey *string) error {
	cred, err := c.credentials(m, hostKey)
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
	fetch := mirror.Fetch
	if m.Mirror {
		fetch = mirror.FetchPrune
	}
	res, err := fetch(ctx, st, remote)
	if err != nil {
		return err
	}
	if res.DefaultBranch != "" && res.DefaultBranch != repo.DefaultBranch {
		if err := c.Repositories.RepoPres.Update(repo.Id, map[string]any{"default_branch": res.DefaultBranch}); err != nil {
			return err
		}
	}
	if m.LFS && len(res.Updated) > 0 {
		return c.fetchLFS(ctx, repo, m, st, res, cred)
	}
	return nil
}

// fetchLFS fetches the LFS objects the updated refs' tips point to that
// the repository lacks.
func (c *MirrorApp) fetchLFS(ctx context.Context, repo *domain.Repository, m *domain.Mirror, st storer.Storer, res *mirror.Result, cred mirror.Credentials) error {
	var tips []plumbing.Hash
	for _, name := range res.Updated {
		if ref, err := st.Reference(plumbing.ReferenceName(name)); err == nil {
			tips = append(tips, ref.Hash())
		}
	}
	objects, err := mirror.Pointers(st, tips)
	if err != nil {
		return err
	}
	var missing []mirror.LFSObject
	for _, o := range objects {
		if _, ok, err := c.Storage.StatLFSObject(repo.StorageKey, o.Oid); err == nil && !ok {
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	server, err := c.Dialer.LFSServer(ctx, m.URL, cred)
	if err != nil {
		return err
	}
	acc := &LFSAccess{Repo: repo, Write: true}
	return c.Dialer.FetchLFS(ctx, server, missing, func(o mirror.LFSObject, r io.Reader) error {
		return c.LFS.Upload(acc, o.Oid, o.Size, r)
	})
}
