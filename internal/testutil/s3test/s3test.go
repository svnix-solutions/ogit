// Package s3test provides S3 endpoints for tests: an in-memory fake that runs
// with plain `go test`, and a MinIO container (see minio.go) behind the
// `integration` build tag.
package s3test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/labbs/git-server-s3/internal/config"
	"github.com/labbs/git-server-s3/pkg/storage/s3"
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

	faker := gofakes3.New(s3mem.New())
	srv := httptest.NewServer(faker.Server())
	t.Cleanup(srv.Close)

	ep := Endpoint{URL: srv.URL, AccessKey: AccessKey, SecretKey: SecretKey}
	createBucket(t, ep)
	return ep
}

// NewStorage points the global S3 config at ep and returns a configured
// S3Storage. The previous config is restored when the test ends.
func NewStorage(t testing.TB, ep Endpoint) *s3.S3Storage {
	t.Helper()

	prev := config.Storage
	t.Cleanup(func() { config.Storage = prev })

	config.Storage.Type = "s3"
	config.Storage.S3.Bucket = Bucket
	config.Storage.S3.Endpoint = ep.URL
	config.Storage.S3.AccessKey = ep.AccessKey
	config.Storage.S3.SecretKey = ep.SecretKey
	config.Storage.S3.Region = Region

	storage := s3.NewS3Storage(zerolog.New(zerolog.NewTestWriter(t)).Level(zerolog.WarnLevel))
	require.NoError(t, storage.Configure())
	return storage
}

func createBucket(t testing.TB, ep Endpoint) {
	t.Helper()

	prev := config.Storage
	defer func() { config.Storage = prev }()

	config.Storage.S3.Endpoint = ep.URL
	config.Storage.S3.AccessKey = ep.AccessKey
	config.Storage.S3.SecretKey = ep.SecretKey
	config.Storage.S3.Region = Region

	var client s3.S3Config
	client.Logger = zerolog.Nop()
	require.NoError(t, client.Configure())

	_, err := client.Client.CreateBucket(context.Background(), &awss3.CreateBucketInput{
		Bucket: aws.String(Bucket),
	})
	require.NoError(t, err)
}
