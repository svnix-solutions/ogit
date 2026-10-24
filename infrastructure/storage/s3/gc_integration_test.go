//go:build integration

package s3_test

import (
	"testing"

	"github.com/labbs/ogit/infrastructure/storage/s3/s3test"
)

// Batch deletes and LastModified come from the real service here.
func TestGCMergesSmallPacks_MinIO(t *testing.T) { testGCMergesSmallPacks(t, s3test.StartMinIO(t)) }
