//go:build integration

package server

import (
	"testing"

	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
)

func TestGitOverSSH_MinIO(t *testing.T) {
	testGitOverSSH(t, withS3(s3test.StartMinIO(t)))
}
