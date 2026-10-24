//go:build integration

package server

import (
	"testing"

	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
)

func TestGitHTTPAuthorization_MinIO(t *testing.T) {
	testGitHTTPAuthorization(t, withS3(s3test.StartMinIO(t)))
}
