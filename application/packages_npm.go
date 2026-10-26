package application

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/labbs/ogit/domain"
	"golang.org/x/mod/semver"
)

// npm registry rules (<site>/api/packages/npm/):
//   - names are scoped, @<namespace>/<name>: the scope is the user or group
//     that owns the package; unscoped names are refused;
//   - `npm publish` sends one version with its tarball (base64 in the
//     JSON body, so at most packages.max_file_size); the version is
//     immutable once published, and its integrity (sha512) and shasum, if
//     the client sends them, must match the tarball;
//   - the README is taken from the tarball (package/README*), else from
//     the manifest;
//   - dist-tags: publishing sets the given tags ("latest" by default);
//     writers add and remove tags; "latest" can't be removed, and follows
//     the highest remaining version when its version is unpublished;
//   - deprecating (writers) edits a version's "deprecated" message;
//     unpublishing versions or the package needs a package admin;
//   - search lists the npm packages the caller can read.

var (
	ErrNpmName          = errors.New("npm packages must be scoped: @<user or group>/<name>")
	ErrNpmVersionExists = errors.New("cannot publish over a previously published version")
	ErrNpmInvalid       = errors.New("invalid npm publish request")
	ErrNpmIntegrity     = errors.New("the tarball doesn't match its integrity or shasum")
	ErrNpmLatestTag     = errors.New("the latest tag can't be removed")
)

var npmName = regexp.MustCompile(`^@([a-z0-9][a-z0-9._-]*)/([a-z0-9][a-z0-9._-]*)$`)

const npmReadmeMax = 512 << 10

// SplitNpmName splits "@scope/name" into the namespace and package name.
func SplitNpmName(full string) (namespace, name string, err error) {
	m := npmName.FindStringSubmatch(full)
	if m == nil || len(m[2]) > 214 {
		return "", "", ErrNpmName
	}
	return m[1], m[2], nil
}

// NpmPublishDoc is the body of `npm publish` (and of the updates
// `npm deprecate` and `npm unpublish <pkg>@<version>` send).
type NpmPublishDoc struct {
	Name        string                     `json:"name"`
	DistTags    map[string]string          `json:"dist-tags"`
	Versions    map[string]json.RawMessage `json:"versions"`
	Readme      string                     `json:"readme"`
	Attachments map[string]struct {
		Data   string `json:"data"`
		Length int64  `json:"length"`
	} `json:"_attachments"`
}

// npmMeta is what a version stores in its metadata.
type npmMeta struct {
	Manifest  map[string]any `json:"manifest"`
	Readme    string         `json:"readme,omitempty"`
	Integrity string         `json:"integrity"`
	Shasum    string         `json:"shasum"`
}

func readNpmMeta(v *domain.PackageVersion) (*npmMeta, bool) {
	var m npmMeta
	if json.Unmarshal([]byte(v.Metadata), &m) != nil || m.Manifest == nil {
		return nil, false // not finished publishing
	}
	return &m, true
}

func npmTarball(name, version string) string { return name + "-" + version + ".tgz" }

// NpmPublish publishes a version.
func (c *PackagesApp) NpmPublish(full string, caller PackageCaller, doc *NpmPublishDoc) error {
	ns, name, err := SplitNpmName(full)
	if err != nil {
		return err
	}
	if doc.Name != full || len(doc.Versions) != 1 || len(doc.Attachments) != 1 {
		return ErrNpmInvalid
	}
	var version string
	var manifest map[string]any
	for v, raw := range doc.Versions {
		version = v
		if json.Unmarshal(raw, &manifest) != nil || manifest["name"] != full || manifest["version"] != v {
			return ErrNpmInvalid
		}
	}
	if !packageVersion.MatchString(version) {
		return ErrInvalidPackage
	}
	var data []byte
	for file, a := range doc.Attachments {
		if file != npmTarball(full, version) && file != npmTarball(name, version) {
			return ErrNpmInvalid
		}
		if int64(base64.StdEncoding.DecodedLen(len(a.Data))) > c.maxFileSize()+3 {
			return ErrPackageTooLarge
		}
		if data, err = base64.StdEncoding.DecodeString(a.Data); err != nil {
			return ErrNpmInvalid
		}
	}
	if int64(len(data)) > c.maxFileSize() {
		return ErrPackageTooLarge
	}
	p, err := c.ForWrite(ns, domain.PackageNpm, name, caller)
	if err != nil {
		return err
	}
	if v, err := c.Pers.GetVersion(p.Id, version); err == nil {
		if _, done := readNpmMeta(v); done || v.CreatedAt.After(c.now().Add(-10*time.Minute)) {
			return ErrNpmVersionExists
		}
		// A publish that stopped between the tarball and the manifest
		// (not one in progress): start again.
		if err := c.deleteVersion(p, v); err != nil {
			return err
		}
	}
	s512, s1 := sha512.Sum512(data), sha1.Sum(data)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(s512[:])
	shasum := hex.EncodeToString(s1[:])
	if dist, ok := manifest["dist"].(map[string]any); ok {
		if v, ok := dist["integrity"].(string); ok && v != "" && v != integrity {
			return ErrNpmIntegrity
		}
		if v, ok := dist["shasum"].(string); ok && v != "" && v != shasum {
			return ErrNpmIntegrity
		}
	}
	readme := tarballReadme(data)
	if readme == "" {
		readme, _ = manifest["readme"].(string)
		if readme == "" {
			readme = doc.Readme
		}
	}
	if len(readme) > npmReadmeMax {
		readme = readme[:npmReadmeMax]
	}
	for _, k := range []string{"readme", "readmeFilename", "dist", "_id", "_attachments"} {
		delete(manifest, k)
	}
	f, err := c.PutFile(p, version, npmTarball(name, version), caller, "application/octet-stream", bytes.NewReader(data))
	if errors.Is(err, ErrPackageVersionExists) {
		return ErrNpmVersionExists
	} else if err != nil {
		return err
	}
	meta, err := json.Marshal(npmMeta{Manifest: manifest, Readme: readme, Integrity: integrity, Shasum: shasum})
	if err != nil {
		return err
	}
	if err := c.Pers.UpdateVersionMetadata(f.VersionId, string(meta)); err != nil {
		return err
	}
	tags := map[string]bool{}
	for tag, v := range doc.DistTags {
		if v == version && ValidTag(tag) {
			tags[tag] = true
		}
	}
	if len(tags) == 0 {
		tags["latest"] = true
	}
	for tag := range tags {
		if err := c.Pers.SetTag(&domain.ContainerTag{PackageId: p.Id, Tag: tag, Digest: version, UpdatedAt: c.now().UTC()}); err != nil {
			return err
		}
	}
	p.UpdatedAt = c.now().UTC()
	return c.Pers.UpdatePackage(p, "updated_at")
}

// tarballReadme finds package/README* in a package tarball.
func tarballReadme(data []byte) string {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return ""
		}
		parts := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(parts) == 2 && h.Typeflag == tar.TypeReg && strings.HasPrefix(strings.ToLower(parts[1]), "readme") {
			b, _ := io.ReadAll(io.LimitReader(tr, npmReadmeMax))
			return string(b)
		}
	}
}

// npmPackage finds a readable npm package.
func (c *PackagesApp) npmPackage(full string, caller PackageCaller) (*domain.Package, error) {
	ns, name, err := SplitNpmName(full)
	if err != nil {
		return nil, ErrPackageNotFound
	}
	return c.Readable(ns, domain.PackageNpm, name, caller)
}

type npmVersion struct {
	v    domain.PackageVersion
	meta *npmMeta
}

func (c *PackagesApp) npmVersions(p *domain.Package) ([]npmVersion, map[string]string, error) {
	versions, err := c.Pers.Versions(p.Id)
	if err != nil {
		return nil, nil, err
	}
	var out []npmVersion
	for _, v := range versions {
		if m, ok := readNpmMeta(&v); ok {
			out = append(out, npmVersion{v, m})
		}
	}
	tags, err := c.Pers.Tags(p.Id)
	if err != nil {
		return nil, nil, err
	}
	distTags := map[string]string{}
	for _, t := range tags {
		distTags[t.Tag] = t.Digest
	}
	return out, distTags, nil
}

// NpmPackument is a package's registry document; tarballs are under
// registry (the npm registry's URL, with a trailing slash).
func (c *PackagesApp) NpmPackument(full string, caller PackageCaller, registry string) (map[string]any, error) {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return nil, err
	}
	versions, distTags, err := c.npmVersions(p)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, ErrPackageNotFound
	}
	_, name, _ := SplitNpmName(full)
	docs := map[string]any{}
	times := map[string]any{"created": p.CreatedAt.UTC().Format(time.RFC3339Nano), "modified": p.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	for _, v := range versions {
		m := map[string]any{}
		for k, val := range v.meta.Manifest {
			m[k] = val
		}
		m["_id"] = full + "@" + v.v.Version
		m["dist"] = map[string]any{
			"tarball":   registry + full + "/-/" + npmTarball(name, v.v.Version),
			"integrity": v.meta.Integrity,
			"shasum":    v.meta.Shasum,
		}
		docs[v.v.Version] = m
		times[v.v.Version] = v.v.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	doc := map[string]any{
		"_id": full, "name": full, "_rev": "1-" + hex.EncodeToString([]byte(p.UpdatedAt.UTC().Format("20060102150405"))),
		"dist-tags": distTags, "versions": docs, "time": times,
		"maintainers": []map[string]string{{"name": p.Namespace}},
	}
	if latest, ok := distTags["latest"]; ok {
		for _, v := range versions {
			if v.v.Version == latest {
				doc["readme"] = v.meta.Readme
				if d, ok := v.meta.Manifest["description"]; ok {
					doc["description"] = d
				}
			}
		}
	}
	return doc, nil
}

// NpmTarball opens a version's tarball (file is "<name>-<version>.tgz").
func (c *PackagesApp) NpmTarball(full, file string, caller PackageCaller) (*domain.PackageFile, io.ReadCloser, error) {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return nil, nil, err
	}
	version, ok := strings.CutSuffix(strings.TrimPrefix(file, p.Name+"-"), ".tgz")
	if !ok || !strings.HasPrefix(file, p.Name+"-") {
		return nil, nil, ErrPackageNotFound
	}
	return c.OpenFile(p, version, file)
}

// NpmUpdate applies what `npm deprecate` and `npm unpublish
// <pkg>@<version>` send: the document without the unpublished versions,
// or with changed "deprecated" messages.
func (c *PackagesApp) NpmUpdate(full string, caller PackageCaller, doc *NpmPublishDoc) error {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return err
	}
	versions, _, err := c.npmVersions(p)
	if err != nil {
		return err
	}
	var removed []npmVersion
	var deprecated []npmVersion
	for _, v := range versions {
		raw, ok := doc.Versions[v.v.Version]
		if !ok {
			removed = append(removed, v)
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			return ErrNpmInvalid
		}
		msg, _ := m["deprecated"].(string)
		old, _ := v.meta.Manifest["deprecated"].(string)
		if msg != old {
			if msg == "" {
				delete(v.meta.Manifest, "deprecated")
			} else {
				v.meta.Manifest["deprecated"] = msg
			}
			deprecated = append(deprecated, v)
		}
	}
	if len(removed) > 0 {
		if ok, err := c.canAdmin(p, caller); err != nil || !ok {
			return errOr(err, ErrForbidden)
		}
	}
	if len(deprecated) > 0 {
		if ok, err := c.canWrite(p, caller); err != nil || !ok {
			return errOr(err, ErrForbidden)
		}
	}
	for _, v := range deprecated {
		meta, _ := json.Marshal(v.meta)
		if err := c.Pers.UpdateVersionMetadata(v.v.Id, string(meta)); err != nil {
			return err
		}
	}
	for i := range removed {
		if err := c.deleteVersion(p, &removed[i].v); err != nil {
			return err
		}
	}
	if len(removed) > 0 {
		return c.npmFixLatest(p)
	}
	return nil
}

func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// npmFixLatest points "latest" at the highest release left (else the
// highest version) when its version went.
func (c *PackagesApp) npmFixLatest(p *domain.Package) error {
	if _, err := c.Pers.GetTag(p.Id, "latest"); err == nil {
		return nil
	}
	versions, _, err := c.npmVersions(p)
	if err != nil || len(versions) == 0 {
		return err
	}
	best := ""
	for _, release := range []bool{true, false} {
		for _, v := range versions {
			sv := "v" + v.v.Version
			if !semver.IsValid(sv) || (release && semver.Prerelease(sv) != "") {
				continue
			}
			if best == "" || semver.Compare(sv, "v"+best) > 0 {
				best = v.v.Version
			}
		}
		if best != "" {
			break
		}
	}
	if best == "" {
		best = versions[0].v.Version
	}
	return c.Pers.SetTag(&domain.ContainerTag{PackageId: p.Id, Tag: "latest", Digest: best, UpdatedAt: c.now().UTC()})
}

// NpmDistTags lists a package's dist-tags.
func (c *PackagesApp) NpmDistTags(full string, caller PackageCaller) (map[string]string, error) {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return nil, err
	}
	_, tags, err := c.npmVersions(p)
	return tags, err
}

// NpmSetDistTag points a tag at a published version (writers).
func (c *PackagesApp) NpmSetDistTag(full, tag, version string, caller PackageCaller) error {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return err
	}
	if ok, err := c.canWrite(p, caller); err != nil || !ok {
		return errOr(err, ErrForbidden)
	}
	if !ValidTag(tag) || semver.IsValid("v"+tag) {
		return ErrInvalidPackage // a tag can't look like a version
	}
	if _, err := c.Pers.GetVersion(p.Id, version); err != nil {
		return ErrPackageNotFound
	}
	return c.Pers.SetTag(&domain.ContainerTag{PackageId: p.Id, Tag: tag, Digest: version, UpdatedAt: c.now().UTC()})
}

// NpmDeleteDistTag removes a tag (writers; not latest).
func (c *PackagesApp) NpmDeleteDistTag(full, tag string, caller PackageCaller) error {
	p, err := c.npmPackage(full, caller)
	if err != nil {
		return err
	}
	if ok, err := c.canWrite(p, caller); err != nil || !ok {
		return errOr(err, ErrForbidden)
	}
	if tag == "latest" {
		return ErrNpmLatestTag
	}
	if _, err := c.Pers.GetTag(p.Id, tag); err != nil {
		return ErrPackageNotFound
	}
	return c.Pers.DeleteTag(p.Id, tag)
}

// NpmDeletePackage unpublishes every version (package admins).
func (c *PackagesApp) NpmDeletePackage(full string, caller PackageCaller) error {
	ns, name, err := SplitNpmName(full)
	if err != nil {
		return ErrPackageNotFound
	}
	return c.Delete(ns, domain.PackageNpm, name, caller)
}

// NpmSearchResult is one package found by NpmSearch.
type NpmSearchResult struct {
	Name        string
	Version     string
	Description string
	Keywords    []string
	Date        time.Time
}

// NpmSearch finds readable npm packages by name or scope.
func (c *PackagesApp) NpmSearch(text string, from, size int, caller PackageCaller) ([]NpmSearchResult, int, error) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "@")
	all, err := c.Pers.SearchPackages(domain.PackageNpm, text, 1000)
	if err != nil {
		return nil, 0, err
	}
	var out []NpmSearchResult
	for i := range all {
		p := &all[i]
		if ok, err := c.canRead(p, caller); err != nil || !ok {
			continue
		}
		versions, tags, err := c.npmVersions(p)
		if err != nil || len(versions) == 0 {
			continue
		}
		r := NpmSearchResult{Name: "@" + p.Namespace + "/" + p.Name, Version: tags["latest"], Date: p.UpdatedAt}
		for _, v := range versions {
			if v.v.Version == r.Version {
				r.Description, _ = v.meta.Manifest["description"].(string)
				if kw, ok := v.meta.Manifest["keywords"].([]any); ok {
					for _, k := range kw {
						if s, ok := k.(string); ok {
							r.Keywords = append(r.Keywords, s)
						}
					}
				}
			}
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	total := len(out)
	if from > len(out) {
		from = len(out)
	}
	out = out[from:]
	if size > 0 && size < len(out) {
		out = out[:size]
	}
	return out, total, nil
}

// Username is the caller's username ("" for tokens without a user).
func (c *PackagesApp) Username(caller PackageCaller) string {
	if !domain.IsPerson(caller.UserId) {
		return ""
	}
	u, err := c.Repositories.UserPers.GetById(caller.UserId)
	if err != nil {
		return ""
	}
	return u.Username
}
