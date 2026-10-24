package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-git/go-billy/v5"
	"github.com/labbs/ogit/infrastructure/storage/cache"
)

// blockSize is the unit of ranged reads and of caching: one GET returns up to
// 4 MiB of a pack, so reading many objects costs one request per block, not
// per object.
const blockSize = 4 << 20

// recentBlocks is how many blocks a reader keeps for itself, so that the
// pack decoder's many small seeks do not each hit the shared cache (or S3
// when the cache is "none").
const recentBlocks = 8

// blockReader reads an immutable S3 object (a pack) through ranged GETs of
// blockSize, caching blocks under their object key.
type blockReader struct {
	client *awss3.Client
	bucket string
	key    string
	size   int64
	cache  cache.Cache

	recent []cachedBlock // most recent last
}

type cachedBlock struct {
	index int64
	data  []byte
}

func (r *blockReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) && off < r.size {
		index := off / blockSize
		block, err := r.block(index)
		if err != nil {
			return n, err
		}
		c := copy(p[n:], block[off-index*blockSize:])
		n += c
		off += int64(c)
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *blockReader) block(index int64) ([]byte, error) {
	for _, b := range r.recent {
		if b.index == index {
			return b.data, nil
		}
	}

	ctx := context.Background()
	cacheKey := fmt.Sprintf("%s/%s#%d", r.bucket, r.key, index)
	data, ok := r.cache.Get(ctx, cacheKey)
	if !ok {
		start := index * blockSize
		end := min(start+blockSize, r.size) - 1
		out, err := r.client.GetObject(ctx, &awss3.GetObjectInput{
			Bucket: aws.String(r.bucket),
			Key:    aws.String(r.key),
			Range:  aws.String(fmt.Sprintf("bytes=%d-%d", start, end)),
		})
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", r.key, err)
		}
		defer out.Body.Close()
		data, err = io.ReadAll(out.Body)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != end-start+1 {
			return nil, fmt.Errorf("read %s: short block %d (%d bytes)", r.key, index, len(data))
		}
		r.cache.Set(ctx, cacheKey, data)
	}

	r.recent = append(r.recent, cachedBlock{index: index, data: data})
	if len(r.recent) > recentBlocks {
		r.recent = r.recent[1:]
	}
	return data, nil
}

// packFile presents a blockReader as the read-only billy.File go-git's
// packfile reader expects.
type packFile struct {
	r    *blockReader
	pos  int64
	name string
}

var _ billy.File = (*packFile)(nil)

var errReadOnly = errors.New("pack files on S3 are read-only")

func (f *packFile) Name() string { return f.name }

func (f *packFile) Read(p []byte) (int, error) {
	n, err := f.r.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *packFile) ReadAt(p []byte, off int64) (int, error) { return f.r.ReadAt(p, off) }

func (f *packFile) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos += offset
	case io.SeekEnd:
		f.pos = f.r.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if f.pos < 0 {
		return 0, os.ErrInvalid
	}
	return f.pos, nil
}

func (f *packFile) Write([]byte) (int, error) { return 0, errReadOnly }
func (f *packFile) Truncate(int64) error      { return errReadOnly }
func (f *packFile) Close() error              { return nil }
func (f *packFile) Lock() error               { return nil }
func (f *packFile) Unlock() error             { return nil }
