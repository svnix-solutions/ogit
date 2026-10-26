package persistence

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2/utils"
	"github.com/labbs/ogit/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type packagesPers struct{ db *gorm.DB }

func NewPackagesPers(db *gorm.DB) *packagesPers { return &packagesPers{db: db} }

func (p *packagesPers) CreatePackage(pkg *domain.Package) error {
	pkg.Id = utils.UUIDv4()
	return p.db.Create(pkg).Error
}

func (p *packagesPers) GetPackage(ownerId, typ, name string) (*domain.Package, error) {
	var pkg domain.Package
	if err := p.db.Where("owner_id = ? AND type = ? AND name = ?", ownerId, typ, name).First(&pkg).Error; err != nil {
		return nil, err
	}
	return &pkg, nil
}

func (p *packagesPers) GetPackageById(id string) (*domain.Package, error) {
	var pkg domain.Package
	if err := p.db.Where("id = ?", id).First(&pkg).Error; err != nil {
		return nil, err
	}
	return &pkg, nil
}

func (p *packagesPers) ListPackages(ownerId, typ string) ([]domain.Package, error) {
	q := p.db.Where("owner_id = ?", ownerId)
	if typ != "" {
		q = q.Where("type = ?", typ)
	}
	var out []domain.Package
	err := q.Order("updated_at DESC").Find(&out).Error
	return out, err
}

func (p *packagesPers) PackagesOfRepository(repositoryId string) ([]domain.Package, error) {
	var out []domain.Package
	err := p.db.Where("repository_id = ?", repositoryId).Order("name").Find(&out).Error
	return out, err
}

func (p *packagesPers) SearchPackages(typ, text string, limit int) ([]domain.Package, error) {
	like := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(strings.ToLower(text)) + "%"
	var out []domain.Package
	err := p.db.Where("type = ? AND (LOWER(name) LIKE ? ESCAPE '\\' OR LOWER(namespace) LIKE ? ESCAPE '\\')", typ, like, like).
		Order("updated_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

func (p *packagesPers) UpdateVersionMetadata(id, metadata string) error {
	return p.db.Model(&domain.PackageVersion{}).Where("id = ?", id).Update("metadata", metadata).Error
}

func (p *packagesPers) UpdatePackage(pkg *domain.Package, fields ...string) error {
	return p.db.Model(pkg).Select(fields).Updates(pkg).Error
}

func deleteVersionRows(tx *gorm.DB, versionIds []string) error {
	if len(versionIds) == 0 {
		return nil
	}
	if err := tx.Where("version_id IN ?", versionIds).Delete(&domain.PackageFile{}).Error; err != nil {
		return err
	}
	if err := tx.Where("version_id IN ?", versionIds).Delete(&domain.ContainerManifestRef{}).Error; err != nil {
		return err
	}
	return tx.Where("id IN ?", versionIds).Delete(&domain.PackageVersion{}).Error
}

func (p *packagesPers) DeletePackage(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		var ids []string
		if err := tx.Model(&domain.PackageVersion{}).Where("package_id = ?", id).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if err := deleteVersionRows(tx, ids); err != nil {
			return err
		}
		if err := tx.Where("package_id = ?", id).Delete(&domain.ContainerTag{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&domain.Package{}).Error
	})
}

func (p *packagesPers) CreateVersion(v *domain.PackageVersion) error {
	v.Id = utils.UUIDv4()
	return p.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(v).Error; err != nil {
			return err
		}
		return tx.Model(&domain.Package{}).Where("id = ?", v.PackageId).Update("updated_at", v.CreatedAt).Error
	})
}

func (p *packagesPers) GetVersion(packageId, version string) (*domain.PackageVersion, error) {
	var v domain.PackageVersion
	if err := p.db.Where("package_id = ? AND version = ?", packageId, version).First(&v).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

func (p *packagesPers) Versions(packageId string) ([]domain.PackageVersion, error) {
	var out []domain.PackageVersion
	err := p.db.Where("package_id = ?", packageId).Order("created_at DESC").Find(&out).Error
	return out, err
}

func (p *packagesPers) DeleteVersion(id string) error {
	return p.db.Transaction(func(tx *gorm.DB) error {
		var v domain.PackageVersion
		if err := tx.Where("id = ?", id).First(&v).Error; err != nil {
			return err
		}
		if err := tx.Where("package_id = ? AND digest = ?", v.PackageId, v.Version).Delete(&domain.ContainerTag{}).Error; err != nil {
			return err
		}
		return deleteVersionRows(tx, []string{id})
	})
}

func (p *packagesPers) CreateFile(f *domain.PackageFile) error {
	f.Id = utils.UUIDv4()
	return p.db.Create(f).Error
}

func (p *packagesPers) GetFile(versionId, name string) (*domain.PackageFile, error) {
	var f domain.PackageFile
	if err := p.db.Where("version_id = ? AND name = ?", versionId, name).First(&f).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

func (p *packagesPers) Files(versionId string) ([]domain.PackageFile, error) {
	var out []domain.PackageFile
	err := p.db.Where("version_id = ?", versionId).Order("name").Find(&out).Error
	return out, err
}

func (p *packagesPers) DeleteFile(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.PackageFile{}).Error
}

func (p *packagesPers) SetTag(t *domain.ContainerTag) error {
	return p.db.Clauses(clause.OnConflict{UpdateAll: true}).Create(t).Error
}

func (p *packagesPers) GetTag(packageId, tag string) (*domain.ContainerTag, error) {
	var t domain.ContainerTag
	if err := p.db.Where("package_id = ? AND tag = ?", packageId, tag).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

func (p *packagesPers) Tags(packageId string) ([]domain.ContainerTag, error) {
	var out []domain.ContainerTag
	err := p.db.Where("package_id = ?", packageId).Order("tag").Find(&out).Error
	return out, err
}

func (p *packagesPers) DeleteTagsOf(packageId, digest string) error {
	return p.db.Where("package_id = ? AND digest = ?", packageId, digest).Delete(&domain.ContainerTag{}).Error
}

func (p *packagesPers) DeleteTag(packageId, tag string) error {
	return p.db.Where("package_id = ? AND tag = ?", packageId, tag).Delete(&domain.ContainerTag{}).Error
}

func (p *packagesPers) ImageReferences(packageId, digest string) bool {
	var n int64
	p.db.Model(&domain.ContainerManifestRef{}).
		Joins("JOIN package_version v ON v.id = container_manifest_ref.version_id").
		Where("v.package_id = ? AND container_manifest_ref.digest = ?", packageId, digest).Count(&n)
	return n > 0
}

func (p *packagesPers) PutBlob(b *domain.ContainerBlob) error {
	return p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(b).Error
}

func (p *packagesPers) GetBlob(ownerId, digest string) (*domain.ContainerBlob, error) {
	var b domain.ContainerBlob
	if err := p.db.Where("owner_id = ? AND digest = ?", ownerId, digest).First(&b).Error; err != nil {
		return nil, err
	}
	return &b, nil
}

func (p *packagesPers) AddRefs(versionId string, digests []string) error {
	for _, d := range digests {
		if err := p.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&domain.ContainerManifestRef{VersionId: versionId, Digest: d}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (p *packagesPers) UnreferencedBlobs(before time.Time, limit int) ([]domain.ContainerBlob, error) {
	var out []domain.ContainerBlob
	err := p.db.Raw(`SELECT b.* FROM container_blob b
		WHERE b.created_at < ?
		AND NOT EXISTS (SELECT 1 FROM container_manifest_ref r
			JOIN package_version v ON v.id = r.version_id
			JOIN package p ON p.id = v.package_id
			WHERE r.digest = b.digest AND p.owner_id = b.owner_id)
		AND NOT EXISTS (SELECT 1 FROM package_version v
			JOIN package p ON p.id = v.package_id
			WHERE v.version = b.digest AND p.owner_id = b.owner_id AND p.type = ?)
		LIMIT ?`, before, domain.PackageContainer, limit).Scan(&out).Error
	return out, err
}

func (p *packagesPers) TouchBlobs(ownerId string, digests []string, now time.Time) error {
	if len(digests) == 0 {
		return nil
	}
	return p.db.Model(&domain.ContainerBlob{}).Where("owner_id = ? AND digest IN ?", ownerId, digests).Update("created_at", now).Error
}

func (p *packagesPers) DeleteBlob(ownerId, digest string, before time.Time) (bool, error) {
	res := p.db.Where("owner_id = ? AND digest = ? AND created_at < ?", ownerId, digest, before).Delete(&domain.ContainerBlob{})
	return res.RowsAffected == 1, res.Error
}

func (p *packagesPers) CreateUpload(u *domain.ContainerUpload) error {
	u.Id = utils.UUIDv4()
	return p.db.Create(u).Error
}

func (p *packagesPers) GetUpload(id string) (*domain.ContainerUpload, error) {
	var u domain.ContainerUpload
	if err := p.db.Where("id = ?", id).First(&u).Error; err != nil {
		return nil, err
	}
	return &u, nil
}

func (p *packagesPers) AddUploadChunk(id string, size, added int64, chunks int) (bool, error) {
	res := p.db.Model(&domain.ContainerUpload{}).Where("id = ? AND size = ? AND chunks = ?", id, size, chunks).
		Updates(map[string]any{"size": size + added, "chunks": chunks + 1})
	return res.RowsAffected == 1, res.Error
}

func (p *packagesPers) DeleteUpload(id string) error {
	return p.db.Where("id = ?", id).Delete(&domain.ContainerUpload{}).Error
}

func (p *packagesPers) StaleUploads(before time.Time, limit int) ([]domain.ContainerUpload, error) {
	var out []domain.ContainerUpload
	err := p.db.Where("created_at < ?", before).Limit(limit).Find(&out).Error
	return out, err
}
