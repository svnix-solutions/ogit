package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-git/go-git/v5/plumbing"
	gitcache "github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
)

// Pack layout: <repo>/objects/pack/pack-<checksum>.pack and .idx, like git.
// The pack is uploaded before its index, so an index means a complete pack.
// Packs never change once written, which makes them safe to cache anywhere
// and lets several servers share a bucket: each request lists the packs
// again (one LIST), so it always sees the latest pushes.
//
// `ogit storage gc` merges small packs; the packs it replaced get a .drop
// marker and are ignored from then on, then deleted after a grace period
// (see gc.go).
//
// Repositories written before packs existed keep their objects loose
// (<repo>/objects/xx/yyyy...); those are still read as a fallback, and
// `ogit storage repack` converts them.

// pack is one pack in the repository, with its index loaded.
type pack struct {
	key  string // object key of the .pack
	size int64
	idx  *idxfile.MemoryIndex
	pf   *packfile.Packfile // opened on first use
}

// objectsState is what one listing found.
type objectsState struct {
	packs    []*pack
	hasLoose bool
}

// state lists the repository's packs and whether it still has loose
// objects. It is computed once per storer, i.e. once per request.
func (s *S3Storer) state() (*objectsState, error) {
	if s.objects != nil {
		return s.objects, nil
	}
	ctx := context.TODO()
	st := &objectsState{}

	// objects/ with a delimiter: "pack/" plus one prefix per loose fan-out
	// directory (objects/ab/...) of the old layout.
	prefixes, err := s.client.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{
		Bucket:    aws.String(s.bucket),
		Prefix:    aws.String(s.getObjectKey("objects") + "/"),
		Delimiter: aws.String("/"),
		MaxKeys:   aws.Int32(2),
	})
	if err != nil {
		return nil, err
	}
	packPrefix := s.getObjectKey("objects/pack") + "/"
	for _, p := range prefixes.CommonPrefixes {
		if aws.ToString(p.Prefix) != packPrefix {
			st.hasLoose = true
		}
	}

	sizes := map[string]int64{}
	dropped := map[string]bool{}
	var idxKeys []string
	pager := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(packPrefix),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			switch {
			case strings.HasSuffix(key, ".pack"):
				sizes[key] = aws.ToInt64(o.Size)
			case strings.HasSuffix(key, ".idx"):
				idxKeys = append(idxKeys, key)
			case strings.HasSuffix(key, dropSuffix):
				dropped[strings.TrimSuffix(key, dropSuffix)+".pack"] = true
			}
		}
	}

	for _, idxKey := range idxKeys {
		packKey := strings.TrimSuffix(idxKey, ".idx") + ".pack"
		size, ok := sizes[packKey]
		if !ok || dropped[packKey] {
			continue // index without pack: not usable; dropped: merged by gc
		}
		idx, err := s.loadIndex(ctx, idxKey)
		if err != nil {
			return nil, err
		}
		st.packs = append(st.packs, &pack{key: packKey, size: size, idx: idx})
	}

	s.objects = st
	return st, nil
}

func (s *S3Storer) loadIndex(ctx context.Context, key string) (*idxfile.MemoryIndex, error) {
	cacheKey := s.bucket + "/" + key
	data, ok := s.cache.Get(ctx, cacheKey)
	if !ok {
		out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", key, err)
		}
		defer out.Body.Close()
		if data, err = io.ReadAll(out.Body); err != nil {
			return nil, err
		}
		s.cache.Set(ctx, cacheKey, data)
	}
	idx := idxfile.NewMemoryIndex()
	if err := idxfile.NewDecoder(bytes.NewReader(data)).Decode(idx); err != nil {
		return nil, fmt.Errorf("decode %s: %w", key, err)
	}
	return idx, nil
}

// packfile opens p for reading objects.
func (s *S3Storer) packfile(p *pack) *packfile.Packfile {
	if p.pf == nil {
		file := &packFile{
			r:    &blockReader{client: s.client, bucket: s.bucket, key: p.key, size: p.size, cache: s.cache},
			name: path.Base(p.key),
		}
		p.pf = packfile.NewPackfileWithCache(p.idx, nil, file, gitcache.NewObjectLRU(16*gitcache.MiByte), 0)
	}
	return p.pf
}

// findInPacks returns the pack holding h, if any.
func (s *S3Storer) findInPacks(h plumbing.Hash) (*pack, bool, error) {
	st, err := s.state()
	if err != nil {
		return nil, false, err
	}
	for _, p := range st.packs {
		if ok, _ := p.idx.Contains(h); ok {
			return p, true, nil
		}
	}
	return nil, false, nil
}

// PackfileWriter lets go-git store a pushed pack as one object instead of
// one S3 object per git object. The pack is staged in a temporary file
// (needed to build its index) and removed after the upload.
func (s *S3Storer) PackfileWriter() (io.WriteCloser, error) {
	f, err := createTemp()
	if err != nil {
		return nil, err
	}
	return &packWriter{s: s, f: f}, nil
}

func createTemp() (*os.File, error) { return os.CreateTemp("", "ogit-pack-*") }

type packWriter struct {
	s   *S3Storer
	f   *os.File
	key string // the uploaded .pack, once Close succeeded
}

func (w *packWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

func (w *packWriter) Close() error {
	defer os.Remove(w.f.Name())
	defer w.f.Close()

	size, err := w.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if size == 0 {
		return nil // nothing was sent (e.g. a push of existing objects)
	}
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	// Index the pack: the pushed pack is self-contained (no-thin).
	var writer idxfile.Writer
	parser, err := packfile.NewParser(packfile.NewScanner(w.f), &writer)
	if err != nil {
		return err
	}
	checksum, err := parser.Parse()
	if err != nil {
		return fmt.Errorf("index pack: %w", err)
	}
	idx, err := writer.Index()
	if err != nil {
		return err
	}
	var idxBuf bytes.Buffer
	if _, err := idxfile.NewEncoder(&idxBuf).Encode(idx); err != nil {
		return err
	}

	base := w.s.getObjectKey("objects/pack/pack-" + checksum.String())
	ctx := context.TODO()
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := w.s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(w.s.bucket), Key: aws.String(base + ".pack"), Body: w.f, ContentLength: aws.Int64(size),
	}); err != nil {
		return fmt.Errorf("upload pack: %w", err)
	}
	// The index last: it marks the pack as complete.
	if _, err := w.s.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(w.s.bucket), Key: aws.String(base + ".idx"), Body: bytes.NewReader(idxBuf.Bytes()),
	}); err != nil {
		return fmt.Errorf("upload pack index: %w", err)
	}

	w.key = base + ".pack"
	w.s.objects = nil // list again: the new pack is visible right away
	return nil
}
