package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labbs/ogit/domain"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"gorm.io/gorm"
)

// Container registry rules (OCI distribution, /v2 on the site's host):
//   - images are <namespace>/<name> (lowercase path components); a
//     container package per image, with its manifests as versions (named
//     by digest) and tags pointing at them;
//   - docker login: username and a personal access token (or GITHUB_TOKEN
//     in Actions); the token endpoint (/v2/token) answers a short-lived
//     JWT carrying the token's hash, checked again on every request;
//     anonymous callers get one too, for public images;
//   - blobs (layers, configs, manifests) are content-addressed per
//     namespace, uploaded in order (chunks stored as they come, assembled
//     and checked against their digest at the end); readers get the blobs
//     an image's manifests reference, writers may check any blob of the
//     namespace (to skip uploads), mounts need the source image readable;
//   - manifests must reference existing blobs (images) or manifests of the
//     same image (indexes);
//   - blobs no manifest references for a day, and uploads older than a
//     day, are deleted (GC).

var (
	ErrBlobUnknown     = errors.New("blob unknown to registry")
	ErrManifestUnknown = errors.New("manifest unknown")
	ErrManifestInvalid = errors.New("manifest invalid")
	ErrDigestInvalid   = errors.New("provided digest did not match uploaded content")
	ErrNameInvalid     = errors.New("invalid repository name")
	ErrUploadUnknown   = errors.New("blob upload unknown to registry")
	ErrUploadRange     = errors.New("chunks must be uploaded in order")
	ErrRegistryAuth    = errors.New("authentication required")
)

const (
	registryTokenLifetime = time.Hour
	maxManifestSize       = 4 << 20
	blobGracePeriod       = 24 * time.Hour
)

var (
	imageComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	tagPattern     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
	digestPattern  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// SplitImage splits "<namespace>/<image>" (the image may have slashes).
func SplitImage(name string) (namespace, image string, err error) {
	ns, image, ok := strings.Cut(name, "/")
	if !ok || len(name) > 255 {
		return "", "", ErrNameInvalid
	}
	for _, part := range strings.Split(image, "/") {
		if !imageComponent.MatchString(part) {
			return "", "", ErrNameInvalid
		}
	}
	return strings.ToLower(ns), image, nil
}

func ValidDigest(d string) bool { return digestPattern.MatchString(d) }
func ValidTag(t string) bool    { return tagPattern.MatchString(t) }

func blobFile(digest string) string { return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:") }

// --- registry tokens ----------------------------------------------------------

type registryClaims struct {
	TokenHash string `json:"th,omitempty"`
	jwt.RegisteredClaims
}

func (c *PackagesApp) registryKey() []byte {
	k := sha256.Sum256([]byte("ogit registry token\x00" + c.Config.Session.SecretKey))
	return k[:]
}

// RegistryToken checks docker login's credentials (password: a personal
// access token or a job token; none: anonymous) and returns a bearer
// token for the registry.
func (c *PackagesApp) RegistryToken(password string) (string, time.Duration, error) {
	claims := registryClaims{RegisteredClaims: jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(c.now()), ExpiresAt: jwt.NewNumericDate(c.now().Add(registryTokenLifetime)),
	}}
	if password != "" {
		if _, err := c.Tokens.Authenticate(password); err != nil {
			return "", 0, err
		}
		claims.TokenHash = HashToken(password)
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.registryKey())
	return s, registryTokenLifetime, err
}

// RegistryCaller checks a registry bearer token and returns who it is
// (the underlying token is checked again: revoked tokens stop working).
func (c *PackagesApp) RegistryCaller(bearer string) (PackageCaller, error) {
	var claims registryClaims
	_, err := jwt.ParseWithClaims(bearer, &claims, func(*jwt.Token) (any, error) { return c.registryKey(), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(c.now))
	if err != nil {
		return PackageCaller{}, ErrRegistryAuth
	}
	if claims.TokenHash == "" {
		return PackageCaller{}, nil // anonymous
	}
	token, err := c.Tokens.AuthenticateHash(claims.TokenHash)
	if err != nil {
		return PackageCaller{}, ErrRegistryAuth
	}
	return callerOf(token), nil
}

// BasicCaller checks a token given directly (Basic auth on registry and
// package routes).
func (c *PackagesApp) BasicCaller(password string) (PackageCaller, error) {
	token, err := c.Tokens.Authenticate(password)
	if err != nil {
		return PackageCaller{}, err
	}
	return callerOf(token), nil
}

func callerOf(token *domain.Token) PackageCaller {
	if token.Type == domain.TokenRepo && token.RepositoryId != nil {
		return PackageCaller{UserId: domain.RepoTokenPrincipal(*token.RepositoryId), Token: token}
	}
	if token.UserId != nil {
		return PackageCaller{UserId: *token.UserId, Token: token}
	}
	return PackageCaller{}
}

// --- images -------------------------------------------------------------------

// Image is a container package the caller reads (or writes).
func (c *PackagesApp) Image(name string, caller PackageCaller, write bool) (*domain.Package, error) {
	ns, image, err := SplitImage(name)
	if err != nil {
		return nil, err
	}
	if write {
		return c.ForWrite(ns, domain.PackageContainer, image, caller)
	}
	return c.Readable(ns, domain.PackageContainer, image, caller)
}

// referenced reports whether a digest is a manifest of the image or
// referenced by one.
func (c *PackagesApp) referenced(p *domain.Package, digest string) bool {
	if _, err := c.Pers.GetVersion(p.Id, digest); err == nil {
		return true
	}
	return c.Pers.ImageReferences(p.Id, digest)
}

// Blob returns a blob readable through an image: one of its manifests'
// blobs, or (writers) any blob of the namespace.
func (c *PackagesApp) Blob(p *domain.Package, digest string, caller PackageCaller) (*domain.ContainerBlob, error) {
	if !ValidDigest(digest) {
		return nil, ErrBlobUnknown
	}
	b, err := c.Pers.GetBlob(p.OwnerId, digest)
	if err != nil {
		return nil, ErrBlobUnknown
	}
	if c.referenced(p, digest) {
		return b, nil
	}
	if ok, err := c.canWrite(p, caller); err != nil || !ok {
		return nil, ErrBlobUnknown
	}
	// A push that finds the blob won't upload it: keep it from GC.
	_ = c.Pers.TouchBlobs(p.OwnerId, []string{digest}, c.now().UTC())
	return b, nil
}

// OpenBlob reads a blob.
func (c *PackagesApp) OpenBlob(b *domain.ContainerBlob) (io.ReadCloser, error) {
	return c.Storage.OpenPackageFile(b.OwnerId, blobFile(b.Digest))
}

// Mount makes a blob of another image of the namespace available (it is
// shared already): the caller must read the source image.
func (c *PackagesApp) Mount(p *domain.Package, digest, from string, caller PackageCaller) bool {
	if !ValidDigest(digest) || from == "" {
		return false
	}
	src, err := c.Image(from, caller, false)
	if err != nil || src.OwnerId != p.OwnerId || !c.referenced(src, digest) {
		return false
	}
	_, err = c.Pers.GetBlob(p.OwnerId, digest)
	return err == nil
}

// StartUpload begins a blob upload into an image's namespace.
func (c *PackagesApp) StartUpload(p *domain.Package) (*domain.ContainerUpload, error) {
	u := &domain.ContainerUpload{OwnerId: p.OwnerId, CreatedAt: c.now().UTC()}
	return u, c.Pers.CreateUpload(u)
}

// Upload finds an upload of an image's namespace.
func (c *PackagesApp) Upload(p *domain.Package, id string) (*domain.ContainerUpload, error) {
	u, err := c.Pers.GetUpload(id)
	if err != nil || u.OwnerId != p.OwnerId {
		return nil, ErrUploadUnknown
	}
	return u, nil
}

func uploadChunk(u *domain.ContainerUpload, i int) string {
	return fmt.Sprintf("uploads/%s/%d", u.Id, i)
}

// AppendUpload stores a chunk at offset (the upload's current size; -1:
// whatever it is).
func (c *PackagesApp) AppendUpload(u *domain.ContainerUpload, offset int64, body io.Reader) (*domain.ContainerUpload, error) {
	if offset >= 0 && offset != u.Size {
		return nil, ErrUploadRange
	}
	h := &hashing{r: body, h: sha256.New(), max: c.maxFileSize() - u.Size}
	name := uploadChunk(u, u.Chunks)
	if err := c.Storage.PutPackageFile(u.OwnerId, name, h); err != nil {
		_ = c.Storage.DeletePackageFile(u.OwnerId, name)
		if errors.Is(err, ErrPackageTooLarge) {
			return nil, ErrPackageTooLarge
		}
		return nil, err
	}
	ok, err := c.Pers.AddUploadChunk(u.Id, u.Size, h.n, u.Chunks)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrUploadRange
	}
	u.Size += h.n
	u.Chunks++
	return u, nil
}

// FinishUpload assembles an upload, checks its digest and stores the blob.
func (c *PackagesApp) FinishUpload(u *domain.ContainerUpload, digest string) (*domain.ContainerBlob, error) {
	if !ValidDigest(digest) {
		return nil, ErrDigestInvalid
	}
	readers := make([]io.Reader, 0, u.Chunks)
	var closers []io.Closer
	defer func() {
		for _, cl := range closers {
			cl.Close()
		}
	}()
	for i := 0; i < u.Chunks; i++ {
		r, err := c.Storage.OpenPackageFile(u.OwnerId, uploadChunk(u, i))
		if err != nil {
			return nil, ErrUploadUnknown
		}
		readers = append(readers, r)
		closers = append(closers, r)
	}
	sum := sha256.New()
	h := &hashing{r: io.MultiReader(readers...), h: sum, max: c.maxFileSize()}
	tmp := fmt.Sprintf("uploads/%s/assembled", u.Id)
	if err := c.Storage.PutPackageFile(u.OwnerId, tmp, h); err != nil {
		_ = c.Storage.DeletePackageFile(u.OwnerId, tmp)
		return nil, err
	}
	defer c.dropUpload(u)
	if "sha256:"+hex.EncodeToString(sum.Sum(nil)) != digest {
		_ = c.Storage.DeletePackageFile(u.OwnerId, tmp)
		return nil, ErrDigestInvalid
	}
	return c.storeBlob(u.OwnerId, digest, h.n, func() (io.ReadCloser, error) { return c.Storage.OpenPackageFile(u.OwnerId, tmp) }, tmp)
}

// storeBlob copies a verified temporary file to the blob's place and
// records it.
func (c *PackagesApp) storeBlob(ownerId, digest string, size int64, open func() (io.ReadCloser, error), tmp string) (*domain.ContainerBlob, error) {
	if b, err := c.Pers.GetBlob(ownerId, digest); err == nil {
		_ = c.Storage.DeletePackageFile(ownerId, tmp)
		_ = c.Pers.TouchBlobs(ownerId, []string{digest}, c.now().UTC())
		return b, nil // already there
	}
	r, err := open()
	if err != nil {
		return nil, err
	}
	err = c.Storage.PutPackageFile(ownerId, blobFile(digest), r)
	r.Close()
	_ = c.Storage.DeletePackageFile(ownerId, tmp)
	if err != nil {
		return nil, err
	}
	b := &domain.ContainerBlob{OwnerId: ownerId, Digest: digest, Size: size, CreatedAt: c.now().UTC()}
	return b, c.Pers.PutBlob(b)
}

func (c *PackagesApp) dropUpload(u *domain.ContainerUpload) {
	for i := 0; i < u.Chunks; i++ {
		_ = c.Storage.DeletePackageFile(u.OwnerId, uploadChunk(u, i))
	}
	_ = c.Pers.DeleteUpload(u.Id)
}

// CancelUpload discards an upload.
func (c *PackagesApp) CancelUpload(u *domain.ContainerUpload) { c.dropUpload(u) }

// --- manifests ----------------------------------------------------------------

// manifestRefs reads what a manifest references: an image's config and
// layers (blobs), or an index's manifests.
func manifestRefs(body []byte) (mediaType string, blobs, manifests []string, err error) {
	var m struct {
		MediaType string `json:"mediaType"`
		Config    *struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
		Subject *struct {
			Digest string `json:"digest"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return "", nil, nil, ErrManifestInvalid
	}
	if m.Config != nil {
		blobs = append(blobs, m.Config.Digest)
	}
	for _, l := range m.Layers {
		blobs = append(blobs, l.Digest)
	}
	for _, x := range m.Manifests {
		manifests = append(manifests, x.Digest)
	}
	for _, d := range append(append([]string{}, blobs...), manifests...) {
		if !ValidDigest(d) {
			return "", nil, nil, ErrManifestInvalid
		}
	}
	if m.Config == nil && m.Manifests == nil {
		return "", nil, nil, ErrManifestInvalid
	}
	return m.MediaType, blobs, manifests, nil
}

// PutManifest stores a manifest under ref (a tag, or its digest).
func (c *PackagesApp) PutManifest(p *domain.Package, ref, contentType string, body []byte, caller PackageCaller) (string, error) {
	if len(body) > maxManifestSize {
		return "", ErrManifestInvalid
	}
	sum := sha256.Sum256(body)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if ValidDigest(ref) {
		if ref != digest {
			return "", ErrDigestInvalid
		}
	} else if !ValidTag(ref) {
		return "", ErrManifestInvalid
	}
	mediaType, blobs, manifests, err := manifestRefs(body)
	if err != nil {
		return "", err
	}
	if contentType == "" || strings.HasPrefix(contentType, "application/json") {
		contentType = mediaType
	}
	if contentType == "" {
		return "", ErrManifestInvalid
	}
	// Touched before they are checked: GC deletes a blob's row only if it
	// wasn't used since its cutoff, before its file, so a blob found here
	// stays.
	if err := c.Pers.TouchBlobs(p.OwnerId, blobs, c.now().UTC()); err != nil {
		return "", err
	}
	for _, d := range blobs {
		if _, err := c.Pers.GetBlob(p.OwnerId, d); err != nil {
			return "", ErrBlobUnknown
		}
	}
	for _, d := range manifests {
		if _, err := c.Pers.GetVersion(p.Id, d); err != nil {
			return "", ErrManifestUnknown
		}
	}
	if _, err := c.Pers.GetBlob(p.OwnerId, digest); err != nil {
		if err := c.Storage.PutPackageFile(p.OwnerId, blobFile(digest), strings.NewReader(string(body))); err != nil {
			return "", err
		}
		if err := c.Pers.PutBlob(&domain.ContainerBlob{OwnerId: p.OwnerId, Digest: digest, Size: int64(len(body)), CreatedAt: c.now().UTC()}); err != nil {
			return "", err
		}
	}
	if _, err := c.Pers.GetVersion(p.Id, digest); err != nil {
		var size int64
		for _, d := range blobs {
			if b, err := c.Pers.GetBlob(p.OwnerId, d); err == nil {
				size += b.Size
			}
		}
		meta, _ := json.Marshal(map[string]any{"media_type": contentType, "manifest_size": len(body)})
		pubId, pubName := publisher(caller)
		v := &domain.PackageVersion{PackageId: p.Id, Version: digest, Metadata: string(meta), Size: size, PublisherId: pubId, PublisherName: pubName, CreatedAt: c.now().UTC()}
		if err := c.Pers.CreateVersion(v); err != nil {
			if helperError.Catch(err) == gorm.ErrDuplicatedKey {
				v, err = c.Pers.GetVersion(p.Id, digest)
			}
			if err != nil {
				return "", err
			}
		}
		if err := c.Pers.AddRefs(v.Id, append(blobs, manifests...)); err != nil {
			return "", err
		}
	}
	if !ValidDigest(ref) {
		if err := c.Pers.SetTag(&domain.ContainerTag{PackageId: p.Id, Tag: ref, Digest: digest, UpdatedAt: c.now().UTC()}); err != nil {
			return "", err
		}
	}
	return digest, nil
}

// Manifest returns a manifest by tag or digest, with its media type.
func (c *PackagesApp) Manifest(p *domain.Package, ref string) (string, string, []byte, error) {
	digest := ref
	if !ValidDigest(ref) {
		t, err := c.Pers.GetTag(p.Id, ref)
		if err != nil {
			return "", "", nil, ErrManifestUnknown
		}
		digest = t.Digest
	}
	v, err := c.Pers.GetVersion(p.Id, digest)
	if err != nil {
		return "", "", nil, ErrManifestUnknown
	}
	var meta struct {
		MediaType string `json:"media_type"`
	}
	_ = json.Unmarshal([]byte(v.Metadata), &meta)
	r, err := c.Storage.OpenPackageFile(p.OwnerId, blobFile(digest))
	if err != nil {
		return "", "", nil, ErrManifestUnknown
	}
	defer r.Close()
	body, err := io.ReadAll(io.LimitReader(r, maxManifestSize))
	return digest, meta.MediaType, body, err
}

// DeleteManifest deletes a manifest (by digest) or a tag (managers).
func (c *PackagesApp) DeleteManifest(p *domain.Package, ref string, caller PackageCaller) error {
	ok, err := c.canAdmin(p, caller)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	if !ValidDigest(ref) {
		if _, err := c.Pers.GetTag(p.Id, ref); err != nil {
			return ErrManifestUnknown
		}
		return c.Pers.DeleteTag(p.Id, ref)
	}
	v, err := c.Pers.GetVersion(p.Id, ref)
	if err != nil {
		return ErrManifestUnknown
	}
	return c.deleteVersion(p, v)
}

// Tags lists an image's tags.
func (c *PackagesApp) Tags(p *domain.Package) ([]string, error) {
	tags, err := c.Pers.Tags(p.Id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, t.Tag)
	}
	return out, nil
}

// CollectGarbage deletes blobs no manifest has referenced for a day, and
// uploads older than a day; it returns how many.
func (c *PackagesApp) CollectGarbage() int {
	before := c.now().UTC().Add(-blobGracePeriod)
	n := 0
	if blobs, err := c.Pers.UnreferencedBlobs(before, 500); err == nil {
		for _, b := range blobs {
			// The row first, unless a push used the blob meanwhile.
			if ok, err := c.Pers.DeleteBlob(b.OwnerId, b.Digest, before); err == nil && ok {
				_ = c.Storage.DeletePackageFile(b.OwnerId, blobFile(b.Digest))
				n++
			}
		}
	}
	if uploads, err := c.Pers.StaleUploads(before, 500); err == nil {
		for i := range uploads {
			c.dropUpload(&uploads[i])
			n++
		}
	}
	return n
}

// Run collects garbage every interval until ctx ends.
func (c *PackagesApp) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := c.CollectGarbage(); n > 0 {
				c.Logger.Info().Str("component", "packages").Int("deleted", n).Msg("container garbage collected")
			}
		}
	}
}
