package application

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/config"
	helperError "github.com/labbs/ogit/infrastructure/helpers/error"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
	"gorm.io/gorm"
)

// Packages rules:
//   - a package belongs to a namespace (a user or a group) and has a type
//     (container, npm, go, generic) and a name, unique per namespace and
//     type; it may be linked to a repository of its namespace;
//   - visibility "repository" follows the linked repository (and is the
//     default for linked packages); unlinked packages start private;
//     public packages are readable by anyone, anonymous included;
//   - reading: the namespace's user, members of the group, readers of the
//     linked repository, and repository tokens (Actions job tokens
//     included) of the linked repository;
//   - publishing: the namespace's user, group maintainers and owners,
//     writers of the linked repository, and the linked repository's
//     tokens; a repository token may create a package in its repository's
//     namespace, linked to it;
//   - managing (visibility, link, deleting versions): the namespace's user,
//     group maintainers and owners, and admins of the linked repository;
//   - tokens need read:packages or write:packages;
//   - files are streamed, at most packages.max_file_size (2 GB), and stored
//     per namespace (storage "packages/<owner id>/"); a version's file is
//     published once.

var (
	ErrPackageNotFound      = errors.New("package not found")
	ErrPackageVersionExists = errors.New("this version already has a file with that name")
	ErrInvalidPackage       = errors.New("invalid package name, version or file name")
	ErrPackageTooLarge      = errors.New("the file is larger than packages.max_file_size")
	ErrPackageScope         = errors.New("the token can't read or publish packages (read:packages, write:packages)")
	ErrInvalidVisibility    = errors.New("visibility must be repository, public or private")
	ErrPackageLink          = errors.New("a package can only be linked to a repository of its namespace")
)

const defaultPackageFileSize = int64(2 << 30)

var (
	packageName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	packageVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,99}$`)
	packageFile    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,199}$`)
)

type PackagesApp struct {
	Config       config.Config
	Logger       zerolog.Logger
	Pers         domain.PackagesPers
	Repositories *RepositoryApp
	Storage      storage.Config
	Tokens       *TokenApp // registry and package tokens
	Now          func() time.Time
}

func (c *PackagesApp) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *PackagesApp) maxFileSize() int64 {
	if c.Config.Packages.MaxFileSize > 0 {
		return c.Config.Packages.MaxFileSize
	}
	return defaultPackageFileSize
}

// PackageCaller is who uses packages: a session user, a personal access
// token (UserId is its user) or a repository token (UserId is the
// repository's principal); anonymous is "".
type PackageCaller = StatusCaller

func (c *PackagesApp) tokenAllows(caller PackageCaller, write bool) bool {
	if caller.Token == nil {
		return true
	}
	if write {
		return caller.tokenAllows(domain.ScopeWritePackages)
	}
	return caller.tokenAllows(domain.ScopeReadPackages, domain.ScopeWritePackages)
}

// namespaceAccess is a user's access to everything of a namespace: owner
// of their own, and their group role mapped like for repositories.
func (c *PackagesApp) namespaceAccess(ns *domain.Namespace, userId string) (domain.AccessType, error) {
	if !domain.IsPerson(userId) {
		return domain.AccessTypeNone, nil
	}
	return c.Repositories.namespaceRole(&domain.Repository{OwnerType: ns.Type, OwnerId: ns.Id}, userId)
}

func packageNamespace(p *domain.Package) *domain.Namespace {
	return &domain.Namespace{Type: p.OwnerType, Id: p.OwnerId, Path: p.Namespace}
}

// linkedAccess is a caller's role on the package's repository.
func (c *PackagesApp) linkedAccess(p *domain.Package, userId string) (domain.AccessType, *domain.Repository, error) {
	if p.RepositoryId == nil || userId == "" {
		return domain.AccessTypeNone, nil, nil
	}
	repo, err := c.Repositories.GetRepository(p.RepositoryId, nil)
	if err != nil {
		return domain.AccessTypeNone, nil, nil // the repository is gone
	}
	if repoId, ok := domain.RepoTokenRepository(userId); ok {
		if repoId == repo.Id {
			return domain.AccessTypeDeveloper, repo, nil // its own packages: the token's scopes decide
		}
		return domain.AccessTypeNone, repo, nil
	}
	role, err := c.Repositories.GetUserEffectiveRole(repo.Id, userId)
	return role, repo, err
}

// IsPublic reports whether anyone can read the package.
func (c *PackagesApp) IsPublic(p *domain.Package) bool {
	switch p.Visibility {
	case domain.PackageVisibilityPublic:
		return true
	case domain.PackageVisibilityRepository:
		if p.RepositoryId == nil {
			return false
		}
		repo, err := c.Repositories.GetRepository(p.RepositoryId, nil)
		return err == nil && !repo.IsPrivate
	}
	return false
}

// access is the caller's role on a package (none: can't read it).
func (c *PackagesApp) access(p *domain.Package, caller PackageCaller) (domain.AccessType, error) {
	ns, err := c.namespaceAccess(packageNamespace(p), caller.UserId)
	if err != nil {
		return domain.AccessTypeNone, err
	}
	linked, _, err := c.linkedAccess(p, caller.UserId)
	if err != nil {
		return domain.AccessTypeNone, err
	}
	role := domain.MaxAccessType(ns, linked)
	if role == domain.AccessTypeNone && c.IsPublic(p) {
		role = domain.AccessTypeReporter
	}
	return role, nil
}

func (c *PackagesApp) canRead(p *domain.Package, caller PackageCaller) (bool, error) {
	if c.IsPublic(p) {
		return true, nil
	}
	if !c.tokenAllows(caller, false) {
		return false, nil
	}
	role, err := c.access(p, caller)
	return role.CanRead(), err
}

// canWrite: publishing new versions. In a group, members (developers on
// its repositories) publish only to packages linked to repositories they
// write to.
func (c *PackagesApp) canWrite(p *domain.Package, caller PackageCaller) (bool, error) {
	if !c.tokenAllows(caller, true) || caller.UserId == "" {
		return false, nil
	}
	ns, err := c.namespaceAccess(packageNamespace(p), caller.UserId)
	if err != nil {
		return false, err
	}
	if ns.CanAdmin() {
		return true, nil
	}
	linked, _, err := c.linkedAccess(p, caller.UserId)
	return linked.CanWrite(), err
}

func (c *PackagesApp) canAdmin(p *domain.Package, caller PackageCaller) (bool, error) {
	if !c.tokenAllows(caller, true) || !domain.IsPerson(caller.UserId) {
		return false, nil
	}
	ns, err := c.namespaceAccess(packageNamespace(p), caller.UserId)
	if err != nil || ns.CanAdmin() {
		return ns.CanAdmin(), err
	}
	linked, _, err := c.linkedAccess(p, caller.UserId)
	return linked.CanAdmin(), err
}

// Readable returns a package the caller can read (missing and unreadable
// look the same).
func (c *PackagesApp) Readable(namespace, typ, name string, caller PackageCaller) (*domain.Package, error) {
	ns, err := c.Repositories.ResolveNamespace(namespace)
	if err != nil {
		return nil, ErrPackageNotFound
	}
	p, err := c.Pers.GetPackage(ns.Id, typ, name)
	if err != nil {
		return nil, ErrPackageNotFound
	}
	ok, err := c.canRead(p, caller)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrPackageNotFound
	}
	return p, nil
}

// ForWrite returns the package to publish to, creating it when the caller
// may: in their namespace, a group they maintain, or (repository tokens)
// their repository's namespace, linked to it.
func (c *PackagesApp) ForWrite(namespace, typ, name string, caller PackageCaller) (*domain.Package, error) {
	if !c.tokenAllows(caller, true) {
		return nil, ErrPackageScope
	}
	if typ != domain.PackageContainer && !ValidPackageName(name) {
		return nil, ErrInvalidPackage
	}
	ns, err := c.Repositories.ResolveNamespace(namespace)
	if err != nil {
		return nil, ErrPackageNotFound
	}
	if p, err := c.Pers.GetPackage(ns.Id, typ, name); err == nil {
		return c.writable(p, caller)
	} else if !isNotFound(err) {
		return nil, err
	}
	p := &domain.Package{OwnerType: ns.Type, OwnerId: ns.Id, Namespace: ns.Path, Type: typ, Name: name,
		Visibility: domain.PackageVisibilityPrivate, CreatedAt: c.now().UTC(), UpdatedAt: c.now().UTC()}
	if repoId, ok := domain.RepoTokenRepository(caller.UserId); ok {
		repo, err := c.Repositories.GetRepository(&repoId, nil)
		if err != nil || repo.OwnerId != ns.Id {
			return nil, ErrPackageNotFound
		}
		p.RepositoryId, p.Visibility = &repo.Id, domain.PackageVisibilityRepository
	} else {
		role, err := c.namespaceAccess(ns, caller.UserId)
		if err != nil {
			return nil, err
		}
		if !role.CanAdmin() {
			return nil, ErrPackageNotFound
		}
	}
	if err := c.Pers.CreatePackage(p); err != nil {
		if helperError.Catch(err) != gorm.ErrDuplicatedKey {
			return nil, err
		}
		existing, err := c.Pers.GetPackage(ns.Id, typ, name) // created meanwhile
		if err != nil {
			return nil, err
		}
		return c.writable(existing, caller)
	}
	c.Logger.Info().Str("component", "application.packages").Str("package", ns.Path+"/"+name).Str("type", typ).Msg("package created")
	return p, nil
}

// writable returns p if the caller publishes to it (else forbidden, or not
// found when they can't read it either).
func (c *PackagesApp) writable(p *domain.Package, caller PackageCaller) (*domain.Package, error) {
	ok, err := c.canWrite(p, caller)
	if err != nil {
		return nil, err
	}
	if !ok {
		if readable, _ := c.canRead(p, caller); readable {
			return nil, ErrForbidden
		}
		return nil, ErrPackageNotFound
	}
	return p, nil
}

// hashing counts and hashes what it reads, refusing more than max bytes.
type hashing struct {
	r   io.Reader
	h   interface{ Write([]byte) (int, error) }
	n   int64
	max int64
}

func (h *hashing) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	h.h.Write(p[:n])
	h.n += int64(n)
	if h.n > h.max {
		return n, ErrPackageTooLarge
	}
	return n, err
}

func versionFile(versionId, name string) string { return "files/" + versionId + "/" + name }

func publisher(caller PackageCaller) (*string, string) {
	if domain.IsPerson(caller.UserId) {
		id := caller.UserId
		return &id, ""
	}
	if caller.Token != nil {
		return nil, caller.Token.Name
	}
	return nil, ""
}

// PutFile publishes a file of a version (created if needed).
func (c *PackagesApp) PutFile(p *domain.Package, version, name string, caller PackageCaller, contentType string, body io.Reader) (*domain.PackageFile, error) {
	if !packageVersion.MatchString(version) || !packageFile.MatchString(name) {
		return nil, ErrInvalidPackage
	}
	v, err := c.Pers.GetVersion(p.Id, version)
	if isNotFound(err) {
		pubId, pubName := publisher(caller)
		v = &domain.PackageVersion{PackageId: p.Id, Version: version, Metadata: "{}", PublisherId: pubId, PublisherName: pubName, CreatedAt: c.now().UTC()}
		if err := c.Pers.CreateVersion(v); err != nil {
			if helperError.Catch(err) != gorm.ErrDuplicatedKey {
				return nil, err
			}
			if v, err = c.Pers.GetVersion(p.Id, version); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	if _, err := c.Pers.GetFile(v.Id, name); err == nil {
		return nil, ErrPackageVersionExists
	}
	sum := sha256.New()
	h := &hashing{r: body, h: sum, max: c.maxFileSize()}
	key := versionFile(v.Id, name)
	if err := c.Storage.PutPackageFile(p.OwnerId, key, h); err != nil {
		_ = c.Storage.DeletePackageFile(p.OwnerId, key)
		if errors.Is(err, ErrPackageTooLarge) {
			return nil, ErrPackageTooLarge
		}
		return nil, err
	}
	f := &domain.PackageFile{VersionId: v.Id, Name: name, Size: h.n, Sha256: hex.EncodeToString(sum.Sum(nil)), ContentType: contentType, CreatedAt: c.now().UTC()}
	if err := c.Pers.CreateFile(f); err != nil {
		_ = c.Storage.DeletePackageFile(p.OwnerId, key)
		if helperError.Catch(err) == gorm.ErrDuplicatedKey {
			return nil, ErrPackageVersionExists
		}
		return nil, err
	}
	return f, nil
}

// OpenFile reads a file of a version.
func (c *PackagesApp) OpenFile(p *domain.Package, version, name string) (*domain.PackageFile, io.ReadCloser, error) {
	v, err := c.Pers.GetVersion(p.Id, version)
	if err != nil {
		return nil, nil, ErrPackageNotFound
	}
	f, err := c.Pers.GetFile(v.Id, name)
	if err != nil {
		return nil, nil, ErrPackageNotFound
	}
	r, err := c.Storage.OpenPackageFile(p.OwnerId, versionFile(v.Id, name))
	if err != nil {
		return nil, nil, err
	}
	return f, r, nil
}

// --- management ---------------------------------------------------------------

// PackageView is a package with what the caller may do.
type PackageView struct {
	*domain.Package
	Public     bool
	CanWrite   bool
	CanAdmin   bool
	Repository *domain.Repository
}

func (c *PackagesApp) view(p *domain.Package, caller PackageCaller) (*PackageView, error) {
	w, err := c.canWrite(p, caller)
	if err != nil {
		return nil, err
	}
	a, err := c.canAdmin(p, caller)
	if err != nil {
		return nil, err
	}
	v := &PackageView{Package: p, Public: c.IsPublic(p), CanWrite: w, CanAdmin: a}
	if p.RepositoryId != nil {
		if repo, err := c.Repositories.GetRepositoryForUser(p.RepositoryId, nil, caller.UserId); err == nil {
			v.Repository = repo
		}
	}
	return v, nil
}

// List lists the packages of a namespace the caller can read.
func (c *PackagesApp) List(namespace, typ string, caller PackageCaller) ([]PackageView, error) {
	ns, err := c.Repositories.ResolveNamespace(namespace)
	if err != nil {
		return nil, err
	}
	all, err := c.Pers.ListPackages(ns.Id, typ)
	if err != nil {
		return nil, err
	}
	out := []PackageView{}
	for i := range all {
		if ok, err := c.canRead(&all[i], caller); err != nil || !ok {
			continue
		}
		v, err := c.view(&all[i], caller)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// ForRepository lists the packages linked to a repository the caller can
// read.
func (c *PackagesApp) ForRepository(id, path *string, caller PackageCaller) ([]PackageView, error) {
	repo, err := c.Repositories.GetRepositoryForUser(id, path, caller.UserId)
	if err != nil {
		return nil, err
	}
	all, err := c.Pers.PackagesOfRepository(repo.Id)
	if err != nil {
		return nil, err
	}
	out := []PackageView{}
	for i := range all {
		if ok, err := c.canRead(&all[i], caller); err != nil || !ok {
			continue
		}
		v, err := c.view(&all[i], caller)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// VersionView is a version with its files (or tags, for images).
type VersionView struct {
	domain.PackageVersion
	Publisher string // username, or the publishing token's name
	Files     []domain.PackageFile
	Tags      []string
}

// Get returns a package with its versions.
func (c *PackagesApp) Get(namespace, typ, name string, caller PackageCaller) (*PackageView, []VersionView, error) {
	p, err := c.Readable(namespace, typ, name, caller)
	if err != nil {
		return nil, nil, err
	}
	v, err := c.view(p, caller)
	if err != nil {
		return nil, nil, err
	}
	versions, err := c.Pers.Versions(p.Id)
	if err != nil {
		return nil, nil, err
	}
	tags := map[string][]string{}
	if p.Type == domain.PackageContainer {
		all, err := c.Pers.Tags(p.Id)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range all {
			tags[t.Digest] = append(tags[t.Digest], t.Tag)
		}
	}
	users := map[string]string{}
	out := make([]VersionView, 0, len(versions))
	for _, ver := range versions {
		vv := VersionView{PackageVersion: ver, Publisher: ver.PublisherName, Tags: tags[ver.Version], Files: []domain.PackageFile{}}
		if ver.PublisherId != nil {
			if _, ok := users[*ver.PublisherId]; !ok {
				if u, err := c.Repositories.UserPers.GetById(*ver.PublisherId); err == nil {
					users[*ver.PublisherId] = u.Username
				}
			}
			vv.Publisher = users[*ver.PublisherId]
		}
		if p.Type != domain.PackageContainer {
			if vv.Files, err = c.Pers.Files(ver.Id); err != nil {
				return nil, nil, err
			}
			vv.Size = 0 // the files' total
			for _, f := range vv.Files {
				vv.Size += f.Size
			}
		}
		if vv.Tags == nil {
			vv.Tags = []string{}
		}
		out = append(out, vv)
	}
	return v, out, nil
}

// PackageChange edits a package's visibility or linked repository ("" to
// unlink).
type PackageChange struct {
	Visibility *string
	Repository *string // <namespace>/<slug> of the same namespace
}

// Update changes a package (its managers).
func (c *PackagesApp) Update(namespace, typ, name string, caller PackageCaller, ch PackageChange) (*PackageView, error) {
	p, err := c.manageable(namespace, typ, name, caller)
	if err != nil {
		return nil, err
	}
	var fields []string
	if ch.Repository != nil {
		if *ch.Repository == "" {
			p.RepositoryId = nil
			if p.Visibility == domain.PackageVisibilityRepository {
				p.Visibility = domain.PackageVisibilityPrivate
				fields = append(fields, "visibility")
			}
		} else {
			path := *ch.Repository
			repo, err := c.Repositories.GetRepositoryForUser(nil, &path, caller.UserId)
			if err != nil || repo.OwnerId != p.OwnerId {
				return nil, ErrPackageLink
			}
			p.RepositoryId = &repo.Id
		}
		fields = append(fields, "repository_id")
	}
	if ch.Visibility != nil {
		switch *ch.Visibility {
		case domain.PackageVisibilityPublic, domain.PackageVisibilityPrivate:
		case domain.PackageVisibilityRepository:
			if p.RepositoryId == nil {
				return nil, ErrInvalidVisibility
			}
		default:
			return nil, ErrInvalidVisibility
		}
		p.Visibility = *ch.Visibility
		fields = append(fields, "visibility")
	}
	if len(fields) > 0 {
		p.UpdatedAt = c.now().UTC()
		if err := c.Pers.UpdatePackage(p, append(fields, "updated_at")...); err != nil {
			return nil, err
		}
	}
	return c.view(p, caller)
}

func (c *PackagesApp) manageable(namespace, typ, name string, caller PackageCaller) (*domain.Package, error) {
	p, err := c.Readable(namespace, typ, name, caller)
	if err != nil {
		return nil, err
	}
	ok, err := c.canAdmin(p, caller)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrForbidden
	}
	return p, nil
}

// DeleteVersion removes a version and its files (container blobs are
// collected later).
func (c *PackagesApp) DeleteVersion(namespace, typ, name, version string, caller PackageCaller) error {
	p, err := c.manageable(namespace, typ, name, caller)
	if err != nil {
		return err
	}
	v, err := c.Pers.GetVersion(p.Id, version)
	if err != nil {
		return ErrPackageNotFound
	}
	return c.deleteVersion(p, v)
}

func (c *PackagesApp) deleteVersion(p *domain.Package, v *domain.PackageVersion) error {
	files, err := c.Pers.Files(v.Id)
	if err != nil {
		return err
	}
	if err := c.Pers.DeleteVersion(v.Id); err != nil {
		return err
	}
	for _, f := range files {
		_ = c.Storage.DeletePackageFile(p.OwnerId, versionFile(v.Id, f.Name))
	}
	return nil
}

// Delete removes a package and its versions.
func (c *PackagesApp) Delete(namespace, typ, name string, caller PackageCaller) error {
	p, err := c.manageable(namespace, typ, name, caller)
	if err != nil {
		return err
	}
	versions, err := c.Pers.Versions(p.Id)
	if err != nil {
		return err
	}
	for i := range versions {
		if err := c.deleteVersion(p, &versions[i]); err != nil {
			return err
		}
	}
	c.Logger.Info().Str("component", "audit").Str("package", p.Namespace+"/"+p.Name).Str("type", p.Type).Str("by", caller.UserId).Msg("package deleted")
	return c.Pers.DeletePackage(p.Id)
}

// ValidPackageName checks a package name (generic, npm without scope).
func ValidPackageName(name string) bool {
	return packageName.MatchString(name) && !strings.Contains(name, "..")
}
