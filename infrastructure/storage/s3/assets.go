package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// Release assets are stored at <repository key>/release-assets/<id>, so
// DeleteRepository removes them; nothing else lists that prefix.

// Actions artifacts and cache entries use the same layout:
// <repository key>/<area>/<id>.
func (s3s *S3Storage) areaKey(repoPath, area, id string) (string, error) {
	if !storageAreas[area] || id == "" || strings.ContainsAny(id, "/\\") || id == "." || id == ".." {
		return "", errors.New("invalid file id")
	}
	return s3s.getRepoKey(repoPath) + "/" + area + "/" + id, nil
}

var storageAreas = map[string]bool{"release-assets": true, "artifacts": true, "actions-cache": true}

func (s3s *S3Storage) PutAsset(repoPath, id string, r io.Reader) error {
	return s3s.PutFile(repoPath, "release-assets", id, r)
}

func (s3s *S3Storage) OpenAsset(repoPath, id string) (io.ReadCloser, error) {
	return s3s.OpenFile(repoPath, "release-assets", id)
}

func (s3s *S3Storage) DeleteAsset(repoPath, id string) error {
	return s3s.DeleteFile(repoPath, "release-assets", id)
}

// PutAsset stages r in a temporary file (S3 needs the length up front),
// then uploads it.
func (s3s *S3Storage) PutFile(repoPath, area, id string, r io.Reader) error {
	key, err := s3s.areaKey(repoPath, area, id)
	if err != nil {
		return err
	}
	if !s3s.RepositoryExists(repoPath) {
		return errors.New("repository does not exist")
	}
	f, err := os.CreateTemp("", "ogit-asset-*")
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

func (s3s *S3Storage) OpenFile(repoPath, area, id string) (io.ReadCloser, error) {
	key, err := s3s.areaKey(repoPath, area, id)
	if err != nil {
		return nil, err
	}
	out, err := s3s.client.GetObject(context.TODO(), &awss3.GetObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}

func (s3s *S3Storage) DeleteFile(repoPath, area, id string) error {
	key, err := s3s.areaKey(repoPath, area, id)
	if err != nil {
		return err
	}
	_, err = s3s.client.DeleteObject(context.TODO(), &awss3.DeleteObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(key)})
	return err
}

func (s3s *S3Storage) OpenFileRange(repoPath, area, id string, offset, length int64) (io.ReadCloser, error) {
	key, err := s3s.areaKey(repoPath, area, id)
	if err != nil {
		return nil, err
	}
	rng := fmt.Sprintf("bytes=%d-%d", offset, offset+length-1)
	out, err := s3s.client.GetObject(context.TODO(), &awss3.GetObjectInput{Bucket: aws.String(s3s.bucket), Key: aws.String(key), Range: aws.String(rng)})
	if err != nil {
		return nil, err
	}
	return out.Body, nil
}
