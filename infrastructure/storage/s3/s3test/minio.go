//go:build integration

package s3test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

// MinIO no longer publishes community images on Docker Hub or quay.io.
// Chainguard's free tier only serves :latest, so it is pinned by digest
// (2026-10-23 build). Bump the digest to pick up a newer MinIO.
const minioImage = "cgr.dev/chainguard/minio:latest@sha256:c47b1779bfd12cdf5a4fcd3bd50671b8d66465fdae45271babc2fb467958b712"

// StartMinIO starts a MinIO container (requires Docker) that is terminated
// when the test ends. The test bucket is created.
func StartMinIO(t testing.TB) Endpoint {
	t.Helper()

	ctx := context.Background()
	ctr, err := minio.Run(ctx, minioImage)
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)

	addr, err := ctr.ConnectionString(ctx)
	require.NoError(t, err)

	ep := Endpoint{URL: "http://" + addr, AccessKey: ctr.Username, SecretKey: ctr.Password}
	createBucket(t, ep)
	return ep
}
