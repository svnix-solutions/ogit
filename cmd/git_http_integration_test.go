//go:build integration

package main

import (
	"testing"

	"github.com/labbs/git-server-s3/internal/testutil/s3test"
)

func TestGitOverHTTP_MinIO(t *testing.T) {
	testGitOverHTTP(t, s3test.NewStorage(t, s3test.StartMinIO(t)))
}
