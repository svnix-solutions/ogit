package git

import (
	"context"
	"testing"

	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	hashA = plumbing.NewHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hashB = plumbing.NewHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	hashC = plumbing.NewHash("cccccccccccccccccccccccccccccccccccccccc")
	main_ = plumbing.ReferenceName("refs/heads/main")
)

func push(t *testing.T, st storer.Storer, cmds ...*packp.Command) *packp.ReportStatus {
	t.Helper()
	sess, expect, err := GuardedReceivePack(st, &transport.Endpoint{Path: "/repo.git"})
	require.NoError(t, err)
	_, err = sess.AdvertisedReferences()
	require.NoError(t, err)

	req := packp.NewReferenceUpdateRequest()
	req.Capabilities.Set(capability.ReportStatus)
	req.Commands = cmds
	expect(req)
	report, _ := sess.ReceivePack(context.Background(), req)
	require.NotNil(t, report)
	return report
}

func status(r *packp.ReportStatus, name plumbing.ReferenceName) string {
	for _, s := range r.CommandStatuses {
		if s.ReferenceName == name {
			return s.Status
		}
	}
	return "missing"
}

// go-git's receive-pack overwrites a ref without checking the old value the
// client sent; the guard turns every write into a compare-and-swap.
func TestGuardedReceivePackChecksOldValue(t *testing.T) {
	for name, newStorer := range map[string]func() storer.Storer{
		"memory":     func() storer.Storer { return memory.NewStorage() },
		"filesystem": func() storer.Storer { return filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault()) },
	} {
		t.Run(name, func(t *testing.T) {
			st := newStorer()
			require.NoError(t, st.SetReference(plumbing.NewHashReference(main_, hashA)))

			r := push(t, st, &packp.Command{Name: main_, Old: hashB, New: hashC})
			assert.NotEqual(t, "ok", status(r, main_), "stale old value is rejected")
			ref, err := st.Reference(main_)
			require.NoError(t, err)
			assert.Equal(t, hashA, ref.Hash(), "and the ref is unchanged")

			r = push(t, st, &packp.Command{Name: main_, Old: hashB, New: plumbing.ZeroHash})
			assert.NotEqual(t, "ok", status(r, main_), "stale delete is rejected")

			r = push(t, st, &packp.Command{Name: main_, Old: hashA, New: hashC})
			assert.Equal(t, "ok", status(r, main_))
			ref, err = st.Reference(main_)
			require.NoError(t, err)
			assert.Equal(t, hashC, ref.Hash())

			r = push(t, st, &packp.Command{Name: main_, Old: hashC, New: plumbing.ZeroHash})
			assert.Equal(t, "ok", status(r, main_), "delete with the right old value")
			_, err = st.Reference(main_)
			assert.ErrorIs(t, err, plumbing.ErrReferenceNotFound)
		})
	}
}

func TestGuardedReceivePackKeepsPackfileWriter(t *testing.T) {
	st := filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault())
	g := &guardedStorer{Storer: st}
	var wrapped storer.Storer = &guardedPackStorer{guardedStorer: g, pw: st}
	_, ok := wrapped.(storer.PackfileWriter)
	assert.True(t, ok, "go-git must still see PackfileWriter, or it unpacks object by object")
}
