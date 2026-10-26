package s3

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// LFS objects are at <repository key>/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>,
// so DeleteRepository removes them; nothing else lists that prefix.

func (s3s *S3Storage) lfsKey(repoPath, oid string) string {
	return s3s.getRepoKey(repoPath) + "/lfs/objects/" + oid[0:2] + "/" + oid[2:4] + "/" + oid
}

// PutLFSObject stages r in a temporary file (S3 needs the length) and
// uploads it.
func (s3s *S3Storage) PutLFSObject(repoPath, oid string, r io.Reader) error {
	if !s3s.RepositoryExists(repoPath) {
		return errors.New("repository does not exist")
	}
	f, err := os.CreateTemp("", "ogit-lfs-*")
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	size, err := io.Copy(f, r)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = s3s.client.PutObject(context.TODO(), &awss3.PutObjectInput{
		Bucket: aws.String(s3s.bucket), Key: aws.String(s3s.lfsKey(repoPath, oid)), Body: f, ContentLength: aws.Int64(size),
	})
	return err
}

func (s3s *S3Storage) OpenLFSObject(repoPath, oid string) (io.ReadCloser, error) {
	out, err := s3s.client.GetObject(context.TODO(), &awss3.GetObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(s3s.lfsKey(repoPath, oid))})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s3s *S3Storage) StatLFSObject(repoPath, oid string) (int64, bool, error) {
	out, err := s3s.client.HeadObject(context.TODO(), &awss3.HeadObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(s3s.lfsKey(repoPath, oid))})
	if err != nil {
		var nf *types.NotFound
		var re *awshttp.ResponseError
		if errors.As(err, &nf) || (errors.As(err, &re) && re.HTTPStatusCode() == 404) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return aws.ToInt64(out.ContentLength), true, nil
}
