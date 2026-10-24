//go:build integration

package s3_test

import (
	"testing"

	"github.com/labbs/git-server-s3/internal/testutil/s3test"
)

func TestS3Storage_MinIO(t *testing.T) {
	testStorageSuite(t, s3test.NewStorage(t, s3test.StartMinIO(t)))
}
