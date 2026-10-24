package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/labbs/ogit/infrastructure/storage/cache"
	"github.com/rs/zerolog"
)

// S3Storer implements go-git's storer.Storer interface using S3 as backend
type S3Storer struct {
	client   *awss3.Client
	bucket   string
	repoPath string
	logger   zerolog.Logger
	cache    cache.Cache

	objects *objectsState // packs found for this request (see packs.go)
}

// NewS3Storer creates a new S3-based storer for a specific repository. blobs
// caches pack blocks and indexes; nil disables caching.
func NewS3Storer(client *awss3.Client, bucket, repoPath string, logger zerolog.Logger, blobs cache.Cache) *S3Storer {
	if blobs == nil {
		blobs = cache.None{}
	}
	return &S3Storer{
		client:   client,
		bucket:   bucket,
		repoPath: repoPath,
		logger:   logger,
		cache:    blobs,
	}
}

// getObjectKey constructs the S3 key for a given path within the repository
func (s *S3Storer) getObjectKey(objectPath string) string {
	return path.Join(s.repoPath, objectPath)
}

// EncodedObject methods

// NewEncodedObject returns a new EncodedObject, the type must be specified
func (s *S3Storer) NewEncodedObject() plumbing.EncodedObject {
	return &plumbing.MemoryObject{}
}

// SetEncodedObject saves an EncodedObject to S3
func (s *S3Storer) SetEncodedObject(obj plumbing.EncodedObject) (plumbing.Hash, error) {
	if obj.Type() == plumbing.OFSDeltaObject || obj.Type() == plumbing.REFDeltaObject {
		return plumbing.ZeroHash, plumbing.ErrInvalidType
	}

	// Read the object content
	reader, err := obj.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer reader.Close()

	content, err := io.ReadAll(reader)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	// Calculate hash if not already set
	hash := obj.Hash()
	if hash == plumbing.ZeroHash {
		obj.SetSize(int64(len(content)))
		hasher := plumbing.NewHasher(obj.Type(), int64(len(content)))
		hasher.Write(content)
		hash = hasher.Sum()
	}

	// Store in S3
	objectKey := s.getObjectKey(fmt.Sprintf("objects/%s/%s", hash.String()[:2], hash.String()[2:]))

	_, err = s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
		Body:   bytes.NewReader(content),
		Metadata: map[string]string{
			"git-type": obj.Type().String(),
		},
	})
	if err == nil && s.objects != nil {
		s.objects.hasLoose = true // readable through this storer right away
	}

	return hash, err
}

// EncodedObject returns the EncodedObject with the given hash, from a pack
// or, for repositories in the old layout, a loose object.
func (s *S3Storer) EncodedObject(t plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	p, ok, err := s.findInPacks(hash)
	if err != nil {
		return nil, err
	}
	if ok {
		obj, err := s.packfile(p).Get(hash)
		if err != nil {
			return nil, err
		}
		if t != plumbing.AnyObject && obj.Type() != t {
			return nil, plumbing.ErrObjectNotFound
		}
		return obj, nil
	}
	if !s.objects.hasLoose {
		return nil, plumbing.ErrObjectNotFound
	}
	return s.looseObject(t, hash)
}

func (s *S3Storer) looseObject(t plumbing.ObjectType, hash plumbing.Hash) (plumbing.EncodedObject, error) {
	objectKey := s.getObjectKey(fmt.Sprintf("objects/%s/%s", hash.String()[:2], hash.String()[2:]))

	result, err := s.client.GetObject(context.TODO(), &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, plumbing.ErrObjectNotFound
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	obj := &plumbing.MemoryObject{}

	// Get the object type from metadata if available
	objectType := t
	if result.Metadata != nil {
		if gitType, exists := result.Metadata["git-type"]; exists {
			switch gitType {
			case "commit":
				objectType = plumbing.CommitObject
			case "tree":
				objectType = plumbing.TreeObject
			case "blob":
				objectType = plumbing.BlobObject
			case "tag":
				objectType = plumbing.TagObject
			}
		}
	}

	obj.SetType(objectType)
	obj.SetSize(int64(len(content)))
	obj.Write(content)

	return obj, nil
}

// IterEncodedObjects returns an iterator for all the objects in the repository
func (s *S3Storer) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	st, err := s.state()
	if err != nil {
		return nil, err
	}
	var objects []plumbing.EncodedObject
	for _, p := range st.packs {
		iter, err := s.packfile(p).GetByType(t)
		if err != nil {
			return nil, err
		}
		if err := iter.ForEach(func(o plumbing.EncodedObject) error {
			objects = append(objects, o)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if !st.hasLoose {
		return storer.NewEncodedObjectSliceIter(objects), nil
	}

	objectsPrefix := s.getObjectKey("objects/")

	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(objectsPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			return nil, err
		}

		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if len(key) < len(objectsPrefix)+3 {
				continue
			}

			// Extract hash from key (objects/ab/cdef...)
			pathParts := strings.Split(key[len(objectsPrefix):], "/")
			if len(pathParts) != 2 {
				continue
			}
			hashStr := pathParts[0] + pathParts[1]

			hash := plumbing.NewHash(hashStr)
			encodedObj, err := s.EncodedObject(t, hash)
			if err == nil && (t == plumbing.AnyObject || encodedObj.Type() == t) {
				objects = append(objects, encodedObj)
			}
		}
	}

	return storer.NewEncodedObjectSliceIter(objects), nil
}

// HasEncodedObject returns nil if the given hash is stored
func (s *S3Storer) HasEncodedObject(hash plumbing.Hash) error {
	if _, ok, err := s.findInPacks(hash); err != nil {
		return err
	} else if ok {
		return nil
	}
	if !s.objects.hasLoose {
		return plumbing.ErrObjectNotFound
	}
	objectKey := s.getObjectKey(fmt.Sprintf("objects/%s/%s", hash.String()[:2], hash.String()[2:]))

	_, err := s.client.HeadObject(context.TODO(), &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return plumbing.ErrObjectNotFound
	}

	return nil
}

// EncodedObjectSize returns the size of the encoded object
func (s *S3Storer) EncodedObjectSize(hash plumbing.Hash) (int64, error) {
	if p, ok, err := s.findInPacks(hash); err != nil {
		return 0, err
	} else if ok {
		offset, err := p.idx.FindOffset(hash)
		if err != nil {
			return 0, err
		}
		return s.packfile(p).GetSizeByOffset(offset)
	}
	if !s.objects.hasLoose {
		return 0, plumbing.ErrObjectNotFound
	}
	objectKey := s.getObjectKey(fmt.Sprintf("objects/%s/%s", hash.String()[:2], hash.String()[2:]))

	result, err := s.client.HeadObject(context.TODO(), &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return 0, plumbing.ErrObjectNotFound
	}

	return aws.ToInt64(result.ContentLength), nil
}

// DeleteEncodedObject removes the encoded object from S3
func (s *S3Storer) DeleteEncodedObject(hash plumbing.Hash) error {
	objectKey := s.getObjectKey(fmt.Sprintf("objects/%s/%s", hash.String()[:2], hash.String()[2:]))

	_, err := s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})

	return err
}

// Reference methods

// SetReference stores a reference
func (s *S3Storer) SetReference(ref *plumbing.Reference) error {
	var objectKey string

	if ref.Name().IsRemote() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/remotes/%s", ref.Name().Short()))
	} else if ref.Name().IsBranch() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/heads/%s", ref.Name().Short()))
	} else if ref.Name().IsTag() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/tags/%s", ref.Name().Short()))
	} else {
		objectKey = s.getObjectKey(string(ref.Name()))
	}

	var content string
	if ref.Type() == plumbing.HashReference {
		content = ref.Hash().String()
	} else {
		content = fmt.Sprintf("ref: %s", ref.Target())
	}

	_, err := s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
		Body:   strings.NewReader(content),
	})

	return err
}

// Reference returns the reference for the given name
func (s *S3Storer) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	var objectKey string

	if name.IsRemote() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/remotes/%s", name.Short()))
	} else if name.IsBranch() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/heads/%s", name.Short()))
	} else if name.IsTag() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/tags/%s", name.Short()))
	} else {
		// Normalize reference name by removing leading slash if present
		refName := string(name)
		if strings.HasPrefix(refName, "/") {
			refName = refName[1:]
		}
		objectKey = s.getObjectKey(refName)
	}

	s.logger.Debug().
		Str("name", string(name)).
		Str("objectKey", objectKey).
		Msg("Getting reference from S3")

	result, err := s.client.GetObject(context.TODO(), &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		s.logger.Debug().
			Err(err).
			Str("name", string(name)).
			Str("objectKey", objectKey).
			Msg("Reference not found in S3")
		return nil, plumbing.ErrReferenceNotFound
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	contentStr := strings.TrimSpace(string(content))

	s.logger.Debug().
		Str("name", string(name)).
		Str("content", contentStr).
		Msg("Reference content from S3")

	if strings.HasPrefix(contentStr, "ref: ") {
		target := plumbing.ReferenceName(strings.TrimPrefix(contentStr, "ref: "))
		return plumbing.NewSymbolicReference(name, target), nil
	}

	hash := plumbing.NewHash(contentStr)
	return plumbing.NewHashReference(name, hash), nil
}

// IterReferences returns an iterator for all references
func (s *S3Storer) IterReferences() (storer.ReferenceIter, error) {
	refsPrefix := s.getObjectKey("refs/")

	var refs []*plumbing.Reference

	// First, add HEAD reference if it exists
	headRef, err := s.Reference(plumbing.HEAD)
	if err == nil {
		refs = append(refs, headRef)
	}

	// Then list all refs/ references
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(refsPrefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			return nil, err
		}

		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			relativePath := key[len(s.getObjectKey("")):]
			// Remove leading slash if present
			if strings.HasPrefix(relativePath, "/") {
				relativePath = relativePath[1:]
			}
			refName := plumbing.ReferenceName(relativePath)

			s.logger.Debug().
				Str("key", key).
				Str("relativePath", relativePath).
				Str("refName", string(refName)).
				Msg("Processing reference in IterReferences")

			ref, err := s.Reference(refName)
			if err == nil {
				refs = append(refs, ref)
			} else {
				s.logger.Debug().
					Err(err).
					Str("refName", string(refName)).
					Msg("Failed to get reference in IterReferences")
			}
		}
	}

	return storer.NewReferenceSliceIter(refs), nil
}

// RemoveReference removes a reference
func (s *S3Storer) RemoveReference(name plumbing.ReferenceName) error {
	var objectKey string

	if name.IsRemote() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/remotes/%s", name.Short()))
	} else if name.IsBranch() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/heads/%s", name.Short()))
	} else if name.IsTag() {
		objectKey = s.getObjectKey(fmt.Sprintf("refs/tags/%s", name.Short()))
	} else {
		objectKey = s.getObjectKey(string(name))
	}

	_, err := s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})

	return err
}

// CountLooseRefs returns the number of loose references
func (s *S3Storer) CountLooseRefs() (int, error) {
	iter, err := s.IterReferences()
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	err = iter.ForEach(func(*plumbing.Reference) error {
		count++
		return nil
	})

	return count, err
}

// Config methods

// Config returns the repository configuration
func (s *S3Storer) Config() (*config.Config, error) {
	objectKey := s.getObjectKey("config")

	result, err := s.client.GetObject(context.TODO(), &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		// Return default config if not found
		return &config.Config{}, nil
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	cfg := &config.Config{}
	err = cfg.Unmarshal(content)
	return cfg, err
}

// SetConfig sets the repository configuration
func (s *S3Storer) SetConfig(cfg *config.Config) error {
	objectKey := s.getObjectKey("config")

	content, err := cfg.Marshal()
	if err != nil {
		return err
	}

	_, err = s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
		Body:   bytes.NewReader(content),
	})

	return err
}

// Index methods

// Index returns the repository index
func (s *S3Storer) Index() (*index.Index, error) {
	// Git bare repositories typically don't have an index
	return &index.Index{}, nil
}

// SetIndex sets the repository index
func (s *S3Storer) SetIndex(idx *index.Index) error {
	// Git bare repositories typically don't have an index
	return nil
}

// Shallow methods

// Shallow returns the shallow commits
func (s *S3Storer) Shallow() ([]plumbing.Hash, error) {
	objectKey := s.getObjectKey("shallow")

	result, err := s.client.GetObject(context.TODO(), &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, nil // No shallow file means no shallow commits
	}
	defer result.Body.Close()

	content, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	var hashes []plumbing.Hash

	for _, line := range lines {
		if line != "" {
			hashes = append(hashes, plumbing.NewHash(line))
		}
	}

	return hashes, nil
}

// SetShallow sets the shallow commits
func (s *S3Storer) SetShallow(hashes []plumbing.Hash) error {
	objectKey := s.getObjectKey("shallow")

	if len(hashes) == 0 {
		// Remove shallow file if no hashes
		_, err := s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(objectKey),
		})
		return err
	}

	var content strings.Builder
	for _, hash := range hashes {
		content.WriteString(hash.String())
		content.WriteString("\n")
	}

	_, err := s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
		Body:   strings.NewReader(content.String()),
	})

	return err
}

// Module returns the submodule storage, which we don't support for S3
func (s *S3Storer) Module(name string) (storer.Storer, error) {
	return nil, fmt.Errorf("submodules not supported in S3 storage")
}

// AddAlternate adds an alternate object database, which we don't support for S3
func (s *S3Storer) AddAlternate(remote string) error {
	return fmt.Errorf("alternates not supported in S3 storage")
}

// Reference updates use S3 conditional requests on the ref object's ETag,
// so they stay atomic with several servers on one bucket: updates
// (If-Match) and creates (If-None-Match) are enforced by AWS S3, MinIO, R2
// and others. Deletes compare the stored value and send If-Match too, but
// some providers (MinIO) ignore it on DELETE, which leaves a small race
// between a delete and a concurrent update of the same ref there.

// refObjectKey is where a reference is stored (same mapping as
// SetReference: the full ref name under the repository).
func (s *S3Storer) refObjectKey(name plumbing.ReferenceName) string {
	return s.getObjectKey(strings.TrimPrefix(string(name), "/"))
}

// readRef returns the stored hash of a reference and its ETag.
func (s *S3Storer) readRef(name plumbing.ReferenceName) (plumbing.Hash, string, error) {
	out, err := s.client.GetObject(context.TODO(), &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(s.refObjectKey(name))})
	if err != nil {
		return plumbing.ZeroHash, "", plumbing.ErrReferenceNotFound
	}
	defer out.Body.Close()
	content, err := io.ReadAll(out.Body)
	if err != nil {
		return plumbing.ZeroHash, "", err
	}
	return plumbing.NewHash(strings.TrimSpace(string(content))), aws.ToString(out.ETag), nil
}

// preconditionFailed reports whether err is S3 refusing a conditional
// request because the object changed (or exists, for If-None-Match).
func preconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}

// CheckAndSetReference sets ref only if the stored value is still old.
func (s *S3Storer) CheckAndSetReference(ref, old *plumbing.Reference) error {
	if old == nil {
		return s.SetReference(ref)
	}
	current, etag, err := s.readRef(old.Name())
	if err != nil {
		return err
	}
	if current != old.Hash() {
		return storage.ErrReferenceHasChanged
	}
	return s.putRefIfMatch(ref, etag)
}

// putRefIfMatch writes ref only if the stored object still has etag: another
// server may have updated it since it was read.
func (s *S3Storer) putRefIfMatch(ref *plumbing.Reference, etag string) error {
	_, err := s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(s.refObjectKey(ref.Name())),
		Body:    strings.NewReader(ref.Hash().String()),
		IfMatch: aws.String(etag),
	})
	if preconditionFailed(err) {
		return storage.ErrReferenceHasChanged
	}
	return err
}

// CreateReference stores ref only if it does not exist yet.
func (s *S3Storer) CreateReference(ref *plumbing.Reference) error {
	_, err := s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.refObjectKey(ref.Name())),
		Body:        strings.NewReader(ref.Hash().String()),
		IfNoneMatch: aws.String("*"),
	})
	if preconditionFailed(err) {
		return storage.ErrReferenceHasChanged
	}
	return err
}

// RemoveReferenceIf deletes the reference only if it still points to old.
func (s *S3Storer) RemoveReferenceIf(name plumbing.ReferenceName, old plumbing.Hash) error {
	current, etag, err := s.readRef(name)
	if err != nil {
		return err
	}
	if current != old {
		return storage.ErrReferenceHasChanged
	}
	return s.deleteRefIfMatch(name, etag)
}

func (s *S3Storer) deleteRefIfMatch(name plumbing.ReferenceName, etag string) error {
	_, err := s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{
		Bucket:  aws.String(s.bucket),
		Key:     aws.String(s.refObjectKey(name)),
		IfMatch: aws.String(etag),
	})
	if preconditionFailed(err) {
		return storage.ErrReferenceHasChanged
	}
	return err
}

// PackRefs packs references into a packed-refs file (not implemented for S3)
func (s *S3Storer) PackRefs() error {
	// S3 storage doesn't need packed refs as each ref is a separate object
	return nil
}
