package s3

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// Package files are stored at packages/<owner id>/<name>, beside
// repositories/.

func (s3s *S3Storage) packageKey(ownerId, name string) (string, error) {
	if ownerId == "" || strings.ContainsAny(ownerId, "/\\") || ownerId == "." || ownerId == ".." {
		return "", errors.New("invalid package owner")
	}
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", errors.New("invalid package path")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid package path")
		}
	}
	return "packages/" + ownerId + "/" + name, nil
}

// PutPackageFile stages r in a temporary file (S3 needs the length), then
// uploads it.
func (s3s *S3Storage) PutPackageFile(ownerId, name string, r io.Reader) error {
	key, err := s3s.packageKey(ownerId, name)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp("", "ogit-package-*")
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
		Bucket: aws.String(s3s.bucket), Key: aws.String(key), Body: f, ContentLength: aws.Int64(size),
	})
	return err
}

func (s3s *S3Storage) OpenPackageFile(ownerId, name string) (io.ReadCloser, error) {
	key, err := s3s.packageKey(ownerId, name)
	if err != nil {
		return nil, err
	}
	out, err := s3s.client.GetObject(context.TODO(), &awss3.GetObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s3s *S3Storage) DeletePackageFile(ownerId, name string) error {
	key, err := s3s.packageKey(ownerId, name)
	if err != nil {
		return err
	}
	_, err = s3s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(key)})
	return err
}
