package mirror

import (
	"context"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A repository without branches never reaches the remote: a forced push
// would delete everything there.
func TestPushNothing(t *testing.T) {
	st := memory.NewStorage()
	_, err := Push(context.Background(), st, Remote{}, PushOptions{Force: true})
	assert.ErrorIs(t, err, ErrNothingToPush)

	// Tags alone don't count either.
	require.NoError(t, st.SetReference(plumbing.NewHashReference("refs/tags/v1", plumbing.NewHash("1111111111111111111111111111111111111111"))))
	_, err = Push(context.Background(), st, Remote{}, PushOptions{Force: true})
	assert.ErrorIs(t, err, ErrNothingToPush)
}

func TestPushResultErr(t *testing.T) {
	assert.NoError(t, (&PushResult{}).Err())
	err := (&PushResult{Rejected: map[string]string{"refs/tags/v1": "moved", "refs/heads/main": "diverged"}}).Err()
	assert.EqualError(t, err, "not pushed: main: diverged; v1: moved")
}
