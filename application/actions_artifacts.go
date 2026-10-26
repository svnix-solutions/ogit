package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labbs/ogit/domain"
)

// Artifacts rules (actions/upload-artifact and download-artifact v4):
//   - a running run uploads and lists its own artifacts with its runtime
//     token (ACTIONS_RUNTIME_TOKEN, a JWT naming the run, signed with a key
//     derived from session.secret_key); the zip goes through short-lived
//     HMAC-signed URLs, in blocks assembled in the order of the final block
//     list, in the repository's "artifacts" area (shared by every server);
//   - at most actions.max_artifact_size bytes; names are unique per run
//     (an unfinished upload of the same name is replaced);
//   - kept actions.artifact_retention_days (the workflow's retention-days
//     can only shorten it); a sweep deletes expired ones;
//   - anyone who can read the repository lists and downloads ready
//     artifacts (the web UI, GitHub's REST API with a token); writers
//     delete them.

var (
	ErrArtifactNotFound   = errors.New("artifact not found")
	ErrArtifactExists     = errors.New("an artifact with this name already exists in this run")
	ErrArtifactTooLarge   = errors.New("the artifact is larger than actions.max_artifact_size")
	ErrInvalidArtifact    = errors.New("artifact names can't contain / \\ : * ? \" < > | or control characters")
	ErrRuntimeToken       = errors.New("invalid runtime token")
	ErrArtifactSignature  = errors.New("invalid or expired link")
	ErrInvalidBlockList   = errors.New("invalid block list")
	defaultArtifactLimit  = int64(2 << 30)
	defaultRetentionDays  = 90
	artifactURLLifetime   = time.Hour
	runtimeTokenLifetime  = 24 * time.Hour
	artifactArea          = "artifacts"
	invalidArtifactName   = regexp.MustCompile(`[/\\:*?"<>|\x00-\x1f]`)
	blockListLatest       = regexp.MustCompile(`<(?:Latest|Uncommitted|Committed)>([^<]*)</(?:Latest|Uncommitted|Committed)>`)
	maxArtifactBlocks     = 50000
	artifactSignatureSalt = "ogit actions artifact\x00"
)

func (c *ActionsApp) maxArtifactSize() int64 {
	if c.Config.Actions.MaxArtifactSize > 0 {
		return c.Config.Actions.MaxArtifactSize
	}
	return defaultArtifactLimit
}

func (c *ActionsApp) retention() time.Duration {
	days := c.Config.Actions.ArtifactRetentionDays
	if days <= 0 {
		days = defaultRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

func (c *ActionsApp) key(purpose string) []byte {
	k := sha256.Sum256([]byte(purpose + c.Config.Session.SecretKey))
	return k[:]
}

// runtimeClaims is the runtime token: GitHub's clients read the run from
// scp ("Actions.Results:<run>:<job>") without checking the signature; the
// server does.
type runtimeClaims struct {
	Scope string `json:"scp"`
	jwt.RegisteredClaims
}

// RuntimeToken is a running run's ACTIONS_RUNTIME_TOKEN.
func (c *ActionsApp) RuntimeToken(run *domain.ActionRun) string {
	now := c.now()
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, runtimeClaims{
		Scope:            fmt.Sprintf("Actions.GenericRead:00000000-0000-0000-0000-000000000000 Actions.Results:%s:%s", run.Id, run.Id),
		RegisteredClaims: jwt.RegisteredClaims{Subject: run.Id, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(runtimeTokenLifetime))},
	})
	s, _ := t.SignedString(c.key("ogit actions runtime\x00"))
	return s
}

// RuntimeRun checks a runtime token and returns its run, which must still
// run.
func (c *ActionsApp) RuntimeRun(token string) (*domain.ActionRun, error) {
	var claims runtimeClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return c.key("ogit actions runtime\x00"), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(c.now))
	if err != nil || claims.Subject == "" {
		return nil, ErrRuntimeToken
	}
	run, err := c.Pers.GetRunById(claims.Subject)
	if err != nil || run.Status != domain.RunRunning {
		return nil, ErrRuntimeToken
	}
	return run, nil
}

func validArtifactName(name string) bool {
	return name != "" && len(name) <= 255 && !invalidArtifactName.MatchString(name) && strings.TrimSpace(name) == name
}

// CreateArtifact starts an upload; it returns the artifact (not ready yet).
func (c *ActionsApp) CreateArtifact(run *domain.ActionRun, name string, expiresAt *time.Time) (*domain.ActionArtifact, error) {
	if !validArtifactName(name) {
		return nil, ErrInvalidArtifact
	}
	if old, err := c.Pers.Artifact(run.Id, name); err == nil {
		if old.Ready {
			return nil, ErrArtifactExists
		}
		c.removeArtifact(old) // an unfinished upload: start again
	}
	now := c.now()
	expires := now.Add(c.retention())
	if expiresAt != nil && expiresAt.After(now) && expiresAt.Before(expires) {
		expires = *expiresAt
	}
	a := &domain.ActionArtifact{RunId: run.Id, RepositoryId: run.RepositoryId, Name: name, ExpiresAt: expires.UTC(), CreatedAt: now.UTC()}
	if err := c.Pers.CreateArtifact(a); err != nil {
		return nil, err
	}
	return a, nil
}

// SignArtifactURL signs op ("UploadArtifact" or "DownloadArtifact") for an
// artifact: the query string of its URL.
func (c *ActionsApp) SignArtifactURL(a *domain.ActionArtifact, op string) string {
	expires := strconv.FormatInt(c.now().Add(artifactURLLifetime).Unix(), 10)
	m := hmac.New(sha256.New, c.key(artifactSignatureSalt))
	fmt.Fprintf(m, "%s\x00%s\x00%s", op, a.Id, expires)
	return "artifact=" + a.Id + "&expires=" + expires + "&sig=" + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// VerifyArtifactURL checks a signed URL's query and returns its artifact.
func (c *ActionsApp) VerifyArtifactURL(op, id, expires, sig string) (*domain.ActionArtifact, error) {
	m := hmac.New(sha256.New, c.key(artifactSignatureSalt))
	fmt.Fprintf(m, "%s\x00%s\x00%s", op, id, expires)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	exp, err2 := strconv.ParseInt(expires, 10, 64)
	if err != nil || err2 != nil || !hmac.Equal(got, m.Sum(nil)) || c.now().Unix() > exp {
		return nil, ErrArtifactSignature
	}
	var a *domain.ActionArtifact
	if a, err = c.artifactById(id); err != nil {
		return nil, ErrArtifactNotFound
	}
	return a, nil
}

func (c *ActionsApp) artifactById(id string) (*domain.ActionArtifact, error) {
	return c.Pers.ArtifactById(id)
}

func (c *ActionsApp) storageKey(repositoryId string) (string, error) {
	repo, err := c.Repositories.GetRepository(&repositoryId, nil)
	if err != nil {
		return "", err
	}
	return repo.StorageKey, nil
}

func blockFile(a *domain.ActionArtifact, blockId string) string {
	return a.Id + ".block." + hex.EncodeToString([]byte(blockId))
}

func zipFile(a *domain.ActionArtifact) string { return a.Id + ".zip" }

// limited refuses more than max bytes.
type limited struct {
	r   io.Reader
	n   int64
	max int64
}

func (l *limited) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.n > l.max {
		return n, ErrArtifactTooLarge
	}
	return n, err
}

// PutArtifactBlock stores one block of an upload (blockId "" stores the
// whole zip at once).
func (c *ActionsApp) PutArtifactBlock(a *domain.ActionArtifact, blockId string, body io.Reader) error {
	if a.Ready {
		return ErrArtifactExists
	}
	key, err := c.storageKey(a.RepositoryId)
	if err != nil {
		return err
	}
	name := zipFile(a)
	if blockId != "" {
		if len(blockId) > 256 {
			return ErrInvalidBlockList
		}
		name = blockFile(a, blockId)
	}
	if blockId != "" {
		// Recorded first: a block cut short is still deleted with the upload.
		if err := c.Pers.AddArtifactBlock(a.Id, blockId); err != nil {
			return err
		}
	}
	l := &limited{r: body, max: c.maxArtifactSize()}
	if err := c.Repositories.StorageConfig.PutFile(key, artifactArea, name, l); err != nil {
		_ = c.Repositories.StorageConfig.DeleteFile(key, artifactArea, name)
		return err
	}
	if blockId == "" {
		return c.Pers.SetArtifactSize(a.Id, l.n)
	}
	return nil
}

// CommitArtifactBlocks assembles the blocks of an upload in the order of
// the block list (Azure's Put Block List XML).
func (c *ActionsApp) CommitArtifactBlocks(a *domain.ActionArtifact, blockList []byte) error {
	if a.Ready {
		return ErrArtifactExists
	}
	var ids []string
	for _, m := range blockListLatest.FindAllSubmatch(blockList, -1) {
		ids = append(ids, string(m[1]))
	}
	if len(ids) == 0 || len(ids) > maxArtifactBlocks {
		return ErrInvalidBlockList
	}
	key, err := c.storageKey(a.RepositoryId)
	if err != nil {
		return err
	}
	blocks := &blockReader{c: c, key: key, a: a, ids: ids}
	l := &limited{r: blocks, max: c.maxArtifactSize()}
	err = c.Repositories.StorageConfig.PutFile(key, artifactArea, zipFile(a), l)
	blocks.close()
	c.deleteBlocks(key, a)
	if err != nil {
		_ = c.Repositories.StorageConfig.DeleteFile(key, artifactArea, zipFile(a))
		return err
	}
	return c.Pers.SetArtifactSize(a.Id, l.n)
}

// blockReader reads the blocks one after the other, opening each only
// when it is reached.
type blockReader struct {
	c   *ActionsApp
	key string
	a   *domain.ActionArtifact
	ids []string
	cur io.ReadCloser
}

func (b *blockReader) Read(p []byte) (int, error) {
	for {
		if b.cur == nil {
			if len(b.ids) == 0 {
				return 0, io.EOF
			}
			r, err := b.c.Repositories.StorageConfig.OpenFile(b.key, artifactArea, blockFile(b.a, b.ids[0]))
			if err != nil {
				return 0, fmt.Errorf("%w: block %q is missing", ErrInvalidBlockList, b.ids[0])
			}
			b.cur, b.ids = r, b.ids[1:]
		}
		n, err := b.cur.Read(p)
		if err == io.EOF {
			b.cur.Close()
			b.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (b *blockReader) close() {
	if b.cur != nil {
		b.cur.Close()
	}
}

// FinalizeArtifact makes an uploaded artifact visible.
func (c *ActionsApp) FinalizeArtifact(run *domain.ActionRun, name string) (*domain.ActionArtifact, error) {
	a, err := c.Pers.Artifact(run.Id, name)
	if err != nil {
		return nil, ErrArtifactNotFound
	}
	key, err := c.storageKey(a.RepositoryId)
	if err != nil {
		return nil, err
	}
	r, err := c.Repositories.StorageConfig.OpenFile(key, artifactArea, zipFile(a))
	if err != nil {
		return nil, ErrArtifactNotFound // nothing was uploaded
	}
	r.Close()
	if err := c.Pers.MarkArtifactReady(a.Id, a.Size); err != nil {
		return nil, err
	}
	a.Ready = true
	return a, nil
}

// RunArtifacts lists a run's ready artifacts, optionally by name.
func (c *ActionsApp) RunArtifacts(run *domain.ActionRun, name string) ([]domain.ActionArtifact, error) {
	list, err := c.Pers.Artifacts(run.Id)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return list, nil
	}
	var out []domain.ActionArtifact
	for _, a := range list {
		if a.Name == name {
			out = append(out, a)
		}
	}
	return out, nil
}

// OwnArtifact is a ready artifact of run, by name.
func (c *ActionsApp) OwnArtifact(run *domain.ActionRun, name string) (*domain.ActionArtifact, error) {
	a, err := c.Pers.Artifact(run.Id, name)
	if err != nil || !a.Ready {
		return nil, ErrArtifactNotFound
	}
	return a, nil
}

// OpenArtifact reads an artifact's zip.
func (c *ActionsApp) OpenArtifact(a *domain.ActionArtifact) (io.ReadCloser, error) {
	if !a.Ready || c.now().After(a.ExpiresAt) {
		return nil, ErrArtifactNotFound
	}
	key, err := c.storageKey(a.RepositoryId)
	if err != nil {
		return nil, err
	}
	return c.Repositories.StorageConfig.OpenFile(key, artifactArea, zipFile(a))
}

// deleteBlocks deletes every stored block of an upload (committed or
// abandoned).
func (c *ActionsApp) deleteBlocks(key string, a *domain.ActionArtifact) {
	ids, err := c.Pers.ArtifactBlocks(a.Id)
	if err != nil {
		return
	}
	for _, id := range ids {
		_ = c.Repositories.StorageConfig.DeleteFile(key, artifactArea, blockFile(a, id))
	}
	_ = c.Pers.ForgetArtifactBlocks(a.Id)
}

func (c *ActionsApp) removeArtifact(a *domain.ActionArtifact) {
	if key, err := c.storageKey(a.RepositoryId); err == nil {
		c.deleteBlocks(key, a)
		_ = c.Repositories.StorageConfig.DeleteFile(key, artifactArea, zipFile(a))
	}
	_ = c.Pers.DeleteArtifact(a.Id)
}

// DeleteOwnArtifact deletes an artifact of the running run (overwrite).
func (c *ActionsApp) DeleteOwnArtifact(run *domain.ActionRun, name string) (*domain.ActionArtifact, error) {
	a, err := c.Pers.Artifact(run.Id, name)
	if err != nil {
		return nil, ErrArtifactNotFound
	}
	c.removeArtifact(a)
	return a, nil
}

// --- readers ---------------------------------------------------------------

// ListRunArtifacts lists a run's artifacts for a reader.
func (c *ActionsApp) ListRunArtifacts(id, path *string, readerId string, number int) ([]domain.ActionArtifact, error) {
	_, run, _, err := c.Run(id, path, readerId, number)
	if err != nil {
		return nil, err
	}
	list, err := c.Pers.Artifacts(run.Id)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, a := range list {
		if c.now().Before(a.ExpiresAt) {
			out = append(out, a)
		}
	}
	return out, nil
}

// ReadableArtifact finds an artifact by its API id for a reader.
func (c *ActionsApp) ReadableArtifact(id, path *string, readerId string, apiId int64) (*domain.Repository, *domain.ActionArtifact, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, readerId)
	if err != nil {
		return nil, nil, err
	}
	a, err := c.Pers.ArtifactByApiId(repo.Id, apiId)
	if err != nil || !a.Ready || c.now().After(a.ExpiresAt) {
		return nil, nil, ErrArtifactNotFound
	}
	return repo, a, nil
}

// RunByApiId finds a run by its API id for a reader.
func (c *ActionsApp) RunByApiId(id, path *string, readerId string, apiId int64) (*domain.Repository, *domain.ActionRun, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, readerId)
	if err != nil {
		return nil, nil, err
	}
	run, err := c.Pers.RunByApiId(repo.Id, apiId)
	if err != nil {
		return nil, nil, ErrRunNotFound
	}
	return repo, run, nil
}

// DeleteArtifact removes an artifact (writers).
func (c *ActionsApp) DeleteArtifact(id, path *string, actorId string, apiId int64) error {
	repo, a, err := c.ReadableArtifact(id, path, actorId, apiId)
	if err != nil {
		return err
	}
	ok, err := c.Repositories.CanUserAccess(repo.Id, actorId, "write")
	if err != nil {
		return err
	}
	if !ok || !domain.IsPerson(actorId) {
		return ErrForbidden
	}
	c.removeArtifact(a)
	return nil
}

// SweepArtifacts deletes expired artifacts; it returns how many.
func (c *ActionsApp) SweepArtifacts() int {
	expired, err := c.Pers.ExpiredArtifacts(c.now().UTC(), 500)
	if err != nil {
		return 0
	}
	for i := range expired {
		c.removeArtifact(&expired[i])
	}
	return len(expired)
}
