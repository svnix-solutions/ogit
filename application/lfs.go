package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
)

// Git LFS rules:
//   - the batch API, object transfers and locks are under
//     /<owner>/<repo>.git/info/lfs, with git's authorization: access tokens
//     (Basic), repository tokens, anonymous reads of public repositories,
//     and short-lived LFS tokens (scoped to one repository and to download
//     or upload) given out by the batch API and by git-lfs-authenticate over
//     SSH (deploy keys included);
//   - downloads need read access, uploads and locks write access (archived
//     repositories refuse them);
//   - objects are stored with the repository (local or S3), streamed, at
//     most lfs.max_object_size (2 GB by default); an upload whose SHA-256
//     or size doesn't match its oid is refused and not kept;
//   - forks serve the objects they lack from the repository they were
//     forked from (if the caller can read it); uploads go to the fork, also
//     of objects its original has;
//   - locks: one per path; only their owner changes the file: pushes that
//     change a path locked by someone else are refused (also by deploy keys
//     and repository tokens). Owners unlock; maintainers and up force-unlock.

var (
	ErrLFSInvalidObject = errors.New("an LFS object needs a sha256 oid (64 hex characters) and a size")
	ErrLFSTooLarge      = errors.New("the object is larger than lfs.max_object_size")
	ErrLFSMismatch      = errors.New("the uploaded content doesn't match the object's oid and size")
	ErrLFSNotFound      = errors.New("object not found")
	ErrLFSBadToken      = errors.New("invalid or expired LFS token")
	ErrLFSLockNotFound  = errors.New("lock not found")
	ErrLFSLockOwner     = errors.New("this lock is someone else's: only maintainers can force-unlock it")
	ErrLFSInvalidPath   = errors.New("a lock needs a file path in the repository")
)

// LFSLockConflict: the path is already locked.
type LFSLockConflict struct{ Lock *domain.LFSLock }

func (e *LFSLockConflict) Error() string { return "already locked by " + e.Lock.Owner.Username }

const (
	DefaultLFSMaxObjectSize = 2 << 30
	lfsTokenLifetime        = time.Hour
	forkChainDepth          = 5
)

var oidPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidOid(oid string) bool { return oidPattern.MatchString(oid) }

type LFSApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Auth         *AuthApp
	Repositories *RepositoryApp
	Storage      storage.Config
	Locks        domain.LFSLockPers
}

// LFSAccess is who uses a repository's LFS API.
type LFSAccess struct {
	Repo   *domain.Repository
	UserId string // a person; "" for deploy keys, repository tokens and anonymous callers
	Name   string // how to name them in errors
	Write  bool   // may upload and lock
	// Limited: write access only to some branches (maintainer edits on a
	// fork): uploads, but no locks.
	Limited bool
}

// MaxObjectSize is lfs.max_object_size.
func (c *LFSApp) MaxObjectSize() int64 {
	if c.Config.LFS.MaxObjectSize > 0 {
		return c.Config.LFS.MaxObjectSize
	}
	return DefaultLFSMaxObjectSize
}

// AuthorizeBasic checks git credentials (nil: anonymous) for reading or,
// with write, uploading and locking.
func (c *LFSApp) AuthorizeBasic(repoPath string, creds *GitCredentials, write bool) (*LFSAccess, error) {
	op := "git-upload-pack"
	if write {
		op = "git-receive-pack"
	}
	repo, pusher, err := c.Auth.AuthorizeGitAs(repoPath, op, creds)
	if err != nil {
		return nil, err
	}
	return &LFSAccess{Repo: repo, UserId: pusher.UserId, Name: pusher.Name, Write: write, Limited: len(pusher.OnlyBranches) > 0}, nil
}

// AccessFor is LFSAccess for an authorized SSH key (git-lfs-authenticate).
func (c *LFSApp) AccessFor(repo *domain.Repository, pusher Pusher, write bool) *LFSAccess {
	return &LFSAccess{Repo: repo, UserId: pusher.UserId, Name: pusher.Name, Write: write, Limited: len(pusher.OnlyBranches) > 0}
}

type lfsToken struct {
	Repo    string `json:"r"`
	User    string `json:"u,omitempty"`
	Name    string `json:"n,omitempty"`
	Write   bool   `json:"w,omitempty"`
	Limited bool   `json:"l,omitempty"`
	Expiry  int64  `json:"e"`
}

func (c *LFSApp) tokenMAC(payload []byte) []byte {
	key := sha256.Sum256([]byte("ogit lfs token\x00" + c.Config.Session.SecretKey))
	m := hmac.New(sha256.New, key[:])
	m.Write(payload)
	return m.Sum(nil)
}

// Token mints a short-lived token for acc's repository and operation.
func (c *LFSApp) Token(acc *LFSAccess) (string, time.Duration) {
	payload, _ := json.Marshal(lfsToken{Repo: acc.Repo.Id, User: acc.UserId, Name: acc.Name, Write: acc.Write, Limited: acc.Limited, Expiry: time.Now().Add(lfsTokenLifetime).Unix()})
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(c.tokenMAC(payload)), lfsTokenLifetime
}

// AuthorizeToken checks an LFS token for repoPath; write needs an upload
// token.
func (c *LFSApp) AuthorizeToken(repoPath, token string, write bool) (*LFSAccess, error) {
	p, s, ok := strings.Cut(token, ".")
	payload, err1 := base64.RawURLEncoding.DecodeString(p)
	mac, err2 := base64.RawURLEncoding.DecodeString(s)
	if !ok || err1 != nil || err2 != nil || !hmac.Equal(mac, c.tokenMAC(payload)) {
		return nil, ErrLFSBadToken
	}
	var t lfsToken
	if json.Unmarshal(payload, &t) != nil || time.Now().Unix() > t.Expiry {
		return nil, ErrLFSBadToken
	}
	repo, err := c.Repositories.GetRepository(nil, &repoPath)
	if err != nil || repo.Id != t.Repo {
		return nil, ErrGitNotFound
	}
	if write && !t.Write {
		return nil, ErrGitForbidden
	}
	if write && repo.IsArchived {
		return nil, ErrGitArchived
	}
	// People's access is checked again (they may have lost it since);
	// anonymous tokens only read public repositories. Deploy keys' and
	// repository tokens' last the token's hour.
	switch {
	case t.User != "":
		if u, err := c.Repositories.UserPers.GetById(t.User); err != nil || !u.Active {
			return nil, ErrLFSBadToken
		}
		if _, err := c.Auth.userGitAccess(repo, t.User, t.Write); err != nil {
			return nil, err
		}
	case t.Name == "" && repo.IsPrivate:
		return nil, ErrGitNotFound
	}
	return &LFSAccess{Repo: repo, UserId: t.User, Name: t.Name, Write: t.Write, Limited: t.Limited}, nil
}

// sources are where acc may read objects from: its repository, then the
// repositories it was forked from that the caller can read.
func (c *LFSApp) sources(acc *LFSAccess) []*domain.Repository {
	out := []*domain.Repository{acc.Repo}
	repo := acc.Repo
	for i := 0; i < forkChainDepth && repo.ForkOfId != nil; i++ {
		orig, err := c.Repositories.GetRepository(repo.ForkOfId, nil)
		if err != nil {
			break
		}
		if ok, err := c.Repositories.CanUserAccess(orig.Id, acc.UserId, "read"); err != nil || !ok {
			break
		}
		out = append(out, orig)
		repo = orig
	}
	return out
}

// find returns the repository holding oid (with that size when size >= 0):
// acc's, or with fallback one it was forked from.
func (c *LFSApp) find(acc *LFSAccess, oid string, size int64, fallback bool) (*domain.Repository, int64, error) {
	repos := []*domain.Repository{acc.Repo}
	if fallback {
		repos = c.sources(acc)
	}
	for _, repo := range repos {
		got, ok, err := c.Storage.StatLFSObject(repo.StorageKey, oid)
		if err != nil {
			return nil, 0, err
		}
		if ok && (size < 0 || got == size) {
			return repo, got, nil
		}
	}
	return nil, 0, ErrLFSNotFound
}

// LFSObject is one object of a batch request, and its outcome.
type LFSObject struct {
	Oid  string
	Size int64
	// Need: download or upload it (else it is there already, for uploads).
	Need bool
	// Err: this object failed (the others may not).
	Err error
}

// Batch decides, for each object, whether to transfer it.
func (c *LFSApp) Batch(acc *LFSAccess, operation string, objects []LFSObject) ([]LFSObject, error) {
	switch operation {
	case "download":
	case "upload":
		if !acc.Write {
			return nil, ErrGitForbidden
		}
	default:
		return nil, fmt.Errorf("%w: operation must be download or upload", ErrLFSInvalidObject)
	}
	out := make([]LFSObject, 0, len(objects))
	for _, o := range objects {
		switch {
		case !ValidOid(o.Oid) || o.Size < 0:
			o.Err = ErrLFSInvalidObject
		case operation == "upload" && o.Size > c.MaxObjectSize():
			o.Err = ErrLFSTooLarge
		default:
			// Uploads complete the repository itself, so a fork doesn't
			// depend on its original for what is pushed to it.
			_, _, err := c.find(acc, o.Oid, o.Size, operation == "download")
			switch {
			case err == nil:
				o.Need = operation == "download"
			case errors.Is(err, ErrLFSNotFound) && operation == "upload":
				o.Need = true
			default:
				o.Err = err
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// verifying passes r through, refusing more than max bytes and, at the
// end, content whose SHA-256 or size isn't oid's.
type verifying struct {
	r    io.Reader
	h    hash.Hash
	n    int64
	max  int64
	oid  string
	size int64 // -1: unknown
}

func (v *verifying) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	v.n += int64(n)
	if v.n > v.max {
		return n, ErrLFSTooLarge
	}
	if err == io.EOF && (hex.EncodeToString(v.h.Sum(nil)) != v.oid || (v.size >= 0 && v.n != v.size)) {
		return n, ErrLFSMismatch
	}
	return n, err
}

// Upload stores an object (size -1 when unknown). The content is checked
// while it is stored: a mismatch leaves nothing behind.
func (c *LFSApp) Upload(acc *LFSAccess, oid string, size int64, body io.Reader) error {
	if !acc.Write {
		return ErrGitForbidden
	}
	if !ValidOid(oid) {
		return ErrLFSInvalidObject
	}
	if size > c.MaxObjectSize() {
		return ErrLFSTooLarge
	}
	v := &verifying{r: body, h: sha256.New(), max: c.MaxObjectSize(), oid: oid, size: size}
	if err := c.Storage.PutLFSObject(acc.Repo.StorageKey, oid, v); err != nil {
		if errors.Is(err, ErrLFSMismatch) || errors.Is(err, ErrLFSTooLarge) {
			return err
		}
		return fmt.Errorf("storing an LFS object: %w", err)
	}
	return nil
}

// Download opens an object, from the repository or the ones it was forked
// from.
func (c *LFSApp) Download(acc *LFSAccess, oid string) (io.ReadCloser, int64, error) {
	if !ValidOid(oid) {
		return nil, 0, ErrLFSNotFound
	}
	repo, size, err := c.find(acc, oid, -1, true)
	if err != nil {
		return nil, 0, err
	}
	r, err := c.Storage.OpenLFSObject(repo.StorageKey, oid)
	return r, size, err
}

// Verify confirms an upload arrived whole.
func (c *LFSApp) Verify(acc *LFSAccess, oid string, size int64) error {
	if !acc.Write {
		return ErrGitForbidden
	}
	if !ValidOid(oid) {
		return ErrLFSInvalidObject
	}
	_, _, err := c.find(acc, oid, size, false)
	return err
}

// lfsReader serves a repository's LFS objects to one reader (web UI, raw
// files, archives), with the fork fallback.
type lfsReader struct {
	c   *LFSApp
	acc *LFSAccess
}

func (r lfsReader) Present(oid string, size int64) bool {
	_, _, err := r.c.find(r.acc, oid, size, true)
	return err == nil
}

func (r lfsReader) Open(oid string, size int64) (io.ReadCloser, error) {
	repo, _, err := r.c.find(r.acc, oid, size, true)
	if err != nil {
		return nil, err
	}
	return r.c.Storage.OpenLFSObject(repo.StorageKey, oid)
}

// Reader serves repo's LFS objects to readerId ("" for anonymous), who can
// read repo.
func (c *LFSApp) Reader(repo *domain.Repository, readerId string) gitbrowse.LFSStore {
	if c == nil {
		return nil
	}
	return lfsReader{c: c, acc: &LFSAccess{Repo: repo, UserId: readerId}}
}

func cleanLockPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) {
		return "", ErrLFSInvalidPath
	}
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return "", ErrLFSInvalidPath
	}
	return p, nil
}
