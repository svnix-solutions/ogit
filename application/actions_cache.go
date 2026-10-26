package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Cache rules (actions/cache, GitHub's v1 cache API: the client uses it on
// every host but github.com):
//   - a running run, with its runtime token, restores entries of its own
//     ref, then of its pull request's base branch, then of the default
//     branch: for each, an exact key, then the newest key with a
//     restore-key prefix, always with the same version (paths and
//     compression); keys are case-insensitive;
//   - it saves to its own ref only, never from a fork; a key, version and
//     ref is saved once (later saves are refused, like GitHub);
//   - entries are uploaded in chunks at their offsets (in parallel),
//     committed once they cover the declared size, and stored in the
//     repository's "actions-cache" area; downloads go through signed
//     URLs, with HEAD and byte ranges;
//   - a repository keeps actions.cache_size_limit bytes (10 GB): the least
//     recently used entries go first; entries unused for 7 days and
//     uploads unfinished for a day are deleted;
//   - maintainers list and delete entries.

var (
	ErrCacheNotFound   = errors.New("cache entry not found")
	ErrCacheExists     = errors.New("this cache key is already saved, or being saved, for this ref")
	ErrCacheReadOnly   = errors.New("runs from forks can't save caches")
	ErrCacheTooLarge   = errors.New("the cache entry is larger than actions.cache_size_limit")
	ErrCacheIncomplete = errors.New("the uploaded chunks don't cover the entry")
	ErrInvalidCacheKey = errors.New("a cache needs a key (at most 512 characters, no commas) and a version")
)

const (
	cacheArea           = "actions-cache"
	cacheUnusedLifetime = 7 * 24 * time.Hour
	cacheUploadLifetime = 24 * time.Hour
	defaultCacheLimit   = int64(10 << 30)
)

func (c *ActionsApp) cacheLimit() int64 {
	if c.Config.Actions.CacheSizeLimit > 0 {
		return c.Config.Actions.CacheSizeLimit
	}
	return defaultCacheLimit
}

// cacheScopes are the refs a run restores from, in order.
func (c *ActionsApp) cacheScopes(run *domain.ActionRun) []string {
	scopes := []string{run.Ref}
	add := func(ref string) {
		for _, s := range scopes {
			if s == ref {
				return
			}
		}
		scopes = append(scopes, ref)
	}
	if run.Event == "pull_request" {
		var p struct {
			PullRequest struct {
				Base struct{ Ref string } `json:"base"`
			} `json:"pull_request"`
		}
		if json.Unmarshal([]byte(run.EventPayload), &p) == nil && p.PullRequest.Base.Ref != "" {
			add("refs/heads/" + p.PullRequest.Base.Ref)
		}
	}
	if repo, err := c.Repositories.GetRepository(&run.RepositoryId, nil); err == nil {
		add("refs/heads/" + repo.DefaultBranch)
	}
	return scopes
}

// LookupCache finds the entry a restore gets (nil: a miss).
func (c *ActionsApp) LookupCache(run *domain.ActionRun, keys []string, version string) (*domain.ActionCache, error) {
	if version == "" || len(keys) == 0 {
		return nil, ErrInvalidCacheKey
	}
	for _, scope := range c.cacheScopes(run) {
		for _, key := range keys {
			key = strings.ToLower(strings.TrimSpace(key))
			if key == "" {
				continue
			}
			for _, prefix := range []bool{false, true} {
				entry, err := c.Pers.CacheEntry(run.RepositoryId, scope, key, version, prefix)
				if isNotFound(err) {
					continue
				} else if err != nil {
					return nil, err
				}
				_ = c.Pers.TouchCache(entry.Id, c.now().UTC())
				return entry, nil
			}
		}
	}
	return nil, nil
}

// ReserveCache starts saving an entry; size may be 0 when unknown.
func (c *ActionsApp) ReserveCache(run *domain.ActionRun, key, version string, size int64) (*domain.ActionCache, error) {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" || len(key) > 512 || strings.Contains(key, ",") || version == "" || len(version) > 512 {
		return nil, ErrInvalidCacheKey
	}
	if run.FromFork {
		return nil, ErrCacheReadOnly
	}
	if size > c.cacheLimit() {
		return nil, ErrCacheTooLarge
	}
	if _, err := c.Pers.CacheExisting(run.RepositoryId, run.Ref, key, version); err == nil {
		return nil, ErrCacheExists
	} else if !isNotFound(err) {
		return nil, err
	}
	now := c.now().UTC()
	entry := &domain.ActionCache{RepositoryId: run.RepositoryId, Scope: run.Ref, CacheKey: key, Version: version, Size: size, CreatedAt: now, LastUsedAt: now}
	if err := c.Pers.CreateCache(entry); err != nil {
		if helperError.Catch(err) == gorm.ErrDuplicatedKey {
			return nil, ErrCacheExists // another job reserved it meanwhile
		}
		return nil, err
	}
	return entry, nil
}

// uploading is an entry being saved by run.
func (c *ActionsApp) uploading(run *domain.ActionRun, apiId int64) (*domain.ActionCache, error) {
	entry, err := c.Pers.CacheByApiId(run.RepositoryId, apiId)
	if err != nil || entry.Scope != run.Ref || entry.Complete {
		return nil, ErrCacheNotFound
	}
	return entry, nil
}

func chunkFile(entry *domain.ActionCache, start int64) string {
	return fmt.Sprintf("%s.chunk.%020d", entry.Id, start)
}

func entryFile(entry *domain.ActionCache) string { return entry.Id }

// UploadCacheChunk stores the bytes of an entry at start.
func (c *ActionsApp) UploadCacheChunk(run *domain.ActionRun, apiId, start int64, body io.Reader) error {
	entry, err := c.uploading(run, apiId)
	if err != nil {
		return err
	}
	if start < 0 || start > c.cacheLimit() {
		return ErrCacheTooLarge
	}
	key, err := c.storageKey(entry.RepositoryId)
	if err != nil {
		return err
	}
	// Recorded first, so a chunk cut short is deleted with the entry.
	if err := c.Pers.AddCacheChunk(&domain.ActionCacheChunk{CacheId: entry.Id, Start: start, Size: -1}); err != nil {
		return err
	}
	l := &limited{r: body, max: c.cacheLimit() - start}
	if err := c.Repositories.StorageConfig.PutFile(key, cacheArea, chunkFile(entry, start), l); err != nil {
		_ = c.Repositories.StorageConfig.DeleteFile(key, cacheArea, chunkFile(entry, start))
		if errors.Is(err, ErrArtifactTooLarge) {
			return ErrCacheTooLarge
		}
		return err
	}
	return c.Pers.AddCacheChunk(&domain.ActionCacheChunk{CacheId: entry.Id, Start: start, Size: l.n})
}

// CommitCache assembles an entry's chunks (which must cover size bytes,
// end to end) and makes it restorable.
func (c *ActionsApp) CommitCache(run *domain.ActionRun, apiId, size int64) error {
	entry, err := c.uploading(run, apiId)
	if err != nil {
		return err
	}
	chunks, err := c.Pers.CacheChunks(entry.Id)
	if err != nil {
		return err
	}
	var at int64
	for _, ch := range chunks {
		if ch.Start != at || ch.Size < 0 {
			return ErrCacheIncomplete
		}
		at += ch.Size
	}
	if at != size || (entry.Size > 0 && at != entry.Size) {
		return ErrCacheIncomplete
	}
	key, err := c.storageKey(entry.RepositoryId)
	if err != nil {
		return err
	}
	r := &chunkReader{c: c, key: key, entry: entry, chunks: chunks}
	err = c.Repositories.StorageConfig.PutFile(key, cacheArea, entryFile(entry), r)
	r.close()
	c.deleteChunks(key, entry)
	if err != nil {
		_ = c.Repositories.StorageConfig.DeleteFile(key, cacheArea, entryFile(entry))
		return err
	}
	if err := c.Pers.CompleteCache(entry.Id, size); err != nil {
		return err
	}
	c.evict(entry.RepositoryId)
	return nil
}

type chunkReader struct {
	c      *ActionsApp
	key    string
	entry  *domain.ActionCache
	chunks []domain.ActionCacheChunk
	cur    io.ReadCloser
}

func (r *chunkReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if len(r.chunks) == 0 {
				return 0, io.EOF
			}
			f, err := r.c.Repositories.StorageConfig.OpenFile(r.key, cacheArea, chunkFile(r.entry, r.chunks[0].Start))
			if err != nil {
				return 0, ErrCacheIncomplete
			}
			r.cur, r.chunks = f, r.chunks[1:]
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			r.cur.Close()
			r.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (r *chunkReader) close() {
	if r.cur != nil {
		r.cur.Close()
	}
}

func (c *ActionsApp) deleteChunks(key string, entry *domain.ActionCache) {
	chunks, err := c.Pers.CacheChunks(entry.Id)
	if err != nil {
		return
	}
	for _, ch := range chunks {
		_ = c.Repositories.StorageConfig.DeleteFile(key, cacheArea, chunkFile(entry, ch.Start))
	}
}

func (c *ActionsApp) removeCache(entry *domain.ActionCache) {
	if key, err := c.storageKey(entry.RepositoryId); err == nil {
		c.deleteChunks(key, entry)
		_ = c.Repositories.StorageConfig.DeleteFile(key, cacheArea, entryFile(entry))
	}
	_ = c.Pers.DeleteCache(entry.Id)
}

// evict deletes a repository's least recently used entries until it is
// within the limit.
func (c *ActionsApp) evict(repositoryId string) int {
	entries, err := c.Pers.Caches(repositoryId) // most recently used first
	if err != nil {
		return 0
	}
	var total int64
	n := 0
	for i := range entries {
		if total+entries[i].Size > c.cacheLimit() {
			c.removeCache(&entries[i])
			n++
			continue
		}
		total += entries[i].Size // kept
	}
	return n
}

// SweepCaches deletes unused entries, abandoned uploads and what is over
// each repository's limit; it returns how many.
func (c *ActionsApp) SweepCaches() int {
	now := c.now().UTC()
	stale, err := c.Pers.StaleCaches(now.Add(-cacheUnusedLifetime), now.Add(-cacheUploadLifetime), 500)
	if err != nil {
		return 0
	}
	for i := range stale {
		c.removeCache(&stale[i])
	}
	n := len(stale)
	if repos, err := c.Pers.CacheRepositoriesOver(c.cacheLimit()); err == nil {
		for _, id := range repos {
			n += c.evict(id)
		}
	}
	return n
}

// SignCacheURL is the query of an entry's download URL (an hour).
func (c *ActionsApp) SignCacheURL(entry *domain.ActionCache) string {
	expires := strconv.FormatInt(c.now().Add(artifactURLLifetime).Unix(), 10)
	m := hmac.New(sha256.New, c.key("ogit actions cache\x00"))
	fmt.Fprintf(m, "%s\x00%s", entry.Id, expires)
	return "entry=" + entry.Id + "&expires=" + expires + "&sig=" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyCacheURL checks a download URL and returns its entry.
func (c *ActionsApp) VerifyCacheURL(id, expires, sig string) (*domain.ActionCache, error) {
	m := hmac.New(sha256.New, c.key("ogit actions cache\x00"))
	fmt.Fprintf(m, "%s\x00%s", id, expires)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	exp, err2 := strconv.ParseInt(expires, 10, 64)
	if err != nil || err2 != nil || !hmac.Equal(got, m.Sum(nil)) || c.now().Unix() > exp {
		return nil, ErrArtifactSignature
	}
	entry, err := c.Pers.CacheById(id)
	if err != nil || !entry.Complete {
		return nil, ErrCacheNotFound
	}
	return entry, nil
}

// OpenCache reads length bytes of an entry from offset.
func (c *ActionsApp) OpenCache(entry *domain.ActionCache, offset, length int64) (io.ReadCloser, error) {
	key, err := c.storageKey(entry.RepositoryId)
	if err != nil {
		return nil, err
	}
	if offset == 0 && length == entry.Size {
		return c.Repositories.StorageConfig.OpenFile(key, cacheArea, entryFile(entry))
	}
	return c.Repositories.StorageConfig.OpenFileRange(key, cacheArea, entryFile(entry), offset, length)
}

// --- maintainers ------------------------------------------------------------

func (c *ActionsApp) cacheAdmin(id, path *string, actorId string) (*domain.Repository, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, actorId)
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
	return repo, nil
}

// ListCaches lists a repository's entries (maintainers), with the limit.
func (c *ActionsApp) ListCaches(id, path *string, actorId string) ([]domain.ActionCache, int64, error) {
	repo, err := c.cacheAdmin(id, path, actorId)
	if err != nil {
		return nil, 0, err
	}
	entries, err := c.Pers.Caches(repo.Id)
	return entries, c.cacheLimit(), err
}

// DeleteCache removes an entry (maintainers).
func (c *ActionsApp) DeleteCache(id, path *string, actorId string, apiId int64) error {
	repo, err := c.cacheAdmin(id, path, actorId)
	if err != nil {
		return err
	}
	entry, err := c.Pers.CacheByApiId(repo.Id, apiId)
	if err != nil {
		return ErrCacheNotFound
	}
	c.removeCache(entry)
	return nil
}
