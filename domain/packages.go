package domain

import "time"

// Package types.
const (
	PackageContainer = "container"
	PackageNpm       = "npm"
	PackageGo        = "go"
	PackageGeneric   = "generic"
)

// Package visibilities: "repository" follows the linked repository.
const (
	PackageVisibilityRepository = "repository"
	PackageVisibilityPublic     = "public"
	PackageVisibilityPrivate    = "private"
)

// Package is a named package of a namespace (user or group), optionally
// linked to a repository.
type Package struct {
	Id           string `gorm:"primaryKey"`
	OwnerType    OwnerType
	OwnerId      string
	Namespace    string
	Type         string
	Name         string
	RepositoryId *string
	Visibility   string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Package) TableName() string { return "package" }

// PackageVersion is a published version (for container images: a
// manifest, named by its digest).
type PackageVersion struct {
	Id            string `gorm:"primaryKey"`
	PackageId     string
	Version       string
	Metadata      string // JSON, per type (a container manifest's media type)
	Size          int64
	PublisherId   *string
	PublisherName string
	CreatedAt     time.Time
}

func (PackageVersion) TableName() string { return "package_version" }

// PackageFile is a file of a version (generic, npm, Go).
type PackageFile struct {
	Id          string `gorm:"primaryKey"`
	VersionId   string
	Name        string
	Size        int64
	Sha256      string
	ContentType string
	CreatedAt   time.Time
}

func (PackageFile) TableName() string { return "package_file" }

// ContainerTag points a tag of an image at a manifest.
type ContainerTag struct {
	PackageId string `gorm:"primaryKey"`
	Tag       string `gorm:"primaryKey"`
	Digest    string
	UpdatedAt time.Time
}

func (ContainerTag) TableName() string { return "container_tag" }

// ContainerBlob is a stored blob (layer, config or manifest) of a
// namespace, by digest. CreatedAt is when it was last stored or used by a
// push: garbage collection waits a day after it.
type ContainerBlob struct {
	OwnerId   string `gorm:"primaryKey"`
	Digest    string `gorm:"primaryKey"`
	Size      int64
	CreatedAt time.Time
}

func (ContainerBlob) TableName() string { return "container_blob" }

// ContainerManifestRef: a manifest (version) references a blob or a
// manifest by digest.
type ContainerManifestRef struct {
	VersionId string `gorm:"primaryKey"`
	Digest    string `gorm:"primaryKey"`
}

func (ContainerManifestRef) TableName() string { return "container_manifest_ref" }

// ContainerUpload is a blob upload session.
type ContainerUpload struct {
	Id        string `gorm:"primaryKey"`
	OwnerId   string
	Size      int64
	Chunks    int // stored at uploads/<id>/<0..Chunks-1>
	CreatedAt time.Time
}

func (ContainerUpload) TableName() string { return "container_upload" }

type PackagesPers interface {
	CreatePackage(p *Package) error
	GetPackage(ownerId, typ, name string) (*Package, error)
	GetPackageById(id string) (*Package, error)
	// ListPackages lists a namespace's packages (typ "" for every type).
	ListPackages(ownerId, typ string) ([]Package, error)
	PackagesOfRepository(repositoryId string) ([]Package, error)
	UpdatePackage(p *Package, fields ...string) error
	// DeletePackage deletes a package with its versions, files and tags.
	DeletePackage(id string) error

	CreateVersion(v *PackageVersion) error
	GetVersion(packageId, version string) (*PackageVersion, error)
	Versions(packageId string) ([]PackageVersion, error)
	// DeleteVersion deletes a version with its files, tags and references.
	DeleteVersion(id string) error

	CreateFile(f *PackageFile) error
	GetFile(versionId, name string) (*PackageFile, error)
	Files(versionId string) ([]PackageFile, error)
	DeleteFile(id string) error

	SetTag(t *ContainerTag) error
	GetTag(packageId, tag string) (*ContainerTag, error)
	Tags(packageId string) ([]ContainerTag, error)
	DeleteTagsOf(packageId, digest string) error
	DeleteTag(packageId, tag string) error
	// ImageReferences reports whether a manifest of the package references
	// digest.
	ImageReferences(packageId, digest string) bool

	PutBlob(b *ContainerBlob) error
	GetBlob(ownerId, digest string) (*ContainerBlob, error)
	// TouchBlobs marks blobs as used now (garbage collection skips them).
	TouchBlobs(ownerId string, digests []string, now time.Time) error
	AddRefs(versionId string, digests []string) error
	// UnreferencedBlobs lists blobs created before before that no manifest
	// of their namespace references (and that are no manifest themselves).
	UnreferencedBlobs(before time.Time, limit int) ([]ContainerBlob, error)
	// DeleteBlob deletes a blob row unless it was used since before.
	DeleteBlob(ownerId, digest string, before time.Time) (bool, error)

	CreateUpload(u *ContainerUpload) error
	GetUpload(id string) (*ContainerUpload, error)
	// AddUploadChunk records a stored chunk if the upload is still at
	// size (chunks come in order); false: someone else appended.
	AddUploadChunk(id string, size, added int64, chunks int) (bool, error)
	DeleteUpload(id string) error
	StaleUploads(before time.Time, limit int) ([]ContainerUpload, error)
}
