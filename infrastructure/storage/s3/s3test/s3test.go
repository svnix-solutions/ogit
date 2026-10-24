// Package s3test provides S3 endpoints for tests: an in-memory fake that runs
// with plain `go test`, and a MinIO container (see minio.go) behind the
// `integration` build tag.
package s3test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/storage/s3"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const (
	Bucket    = "git-test"
	Region    = "us-east-1"
	AccessKey = "test-access-key"
	SecretKey = "test-secret-key"
)

// Endpoint describes an S3-compatible service reachable by the tests.
type Endpoint struct {
	URL       string
	AccessKey string
	SecretKey string
}

// StartFake starts an in-memory S3 server (gofakes3) that is shut down when
// the test ends. The test bucket is created.
func StartFake(t testing.TB) Endpoint {
	t.Helper()

	srv := httptest.NewServer(gofakes3.New(s3mem.New()).Server())
	t.Cleanup(srv.Close)

	ep := Endpoint{URL: srv.URL, AccessKey: AccessKey, SecretKey: SecretKey}
	createBucket(t, ep)
	return ep
}

// Configure points cfg's storage at ep.
func Configure(cfg *config.Config, ep Endpoint) {
	cfg.Storage.Type = "s3"
	cfg.Storage.S3.Bucket = Bucket
	cfg.Storage.S3.Endpoint = ep.URL
	cfg.Storage.S3.AccessKey = ep.AccessKey
	cfg.Storage.S3.SecretKey = ep.SecretKey
	cfg.Storage.S3.Region = Region
}

// NewStorage returns a configured S3Storage backed by ep.
func NewStorage(t testing.TB, ep Endpoint) *s3.S3Storage {
	t.Helper()

	var cfg config.Config
	Configure(&cfg, ep)
	storage := s3.NewS3Storage(zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.WarnLevel), cfg)
	require.NoError(t, storage.Configure())
	return storage
}

// createBucket uses its own client: S3Config keeps its config unexported.
func createBucket(t testing.TB, ep Endpoint) {
	t.Helper()

	client := awss3.New(awss3.Options{
		Region:       Region,
		BaseEndpoint: aws.String(ep.URL),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ep.AccessKey, ep.SecretKey, ""),
	})
	_, err := client.CreateBucket(context.Background(), &awss3.CreateBucketInput{Bucket: aws.String(Bucket)})
	require.NoError(t, err)
}
