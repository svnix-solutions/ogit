// Package mirror fetches remote repositories into a storer (pull mirrors
// and imports): the upload-pack protocol through go-git's transports, as
// git fetch does, since the storers (S3 included) are storer.Storers, not
// the storage.Storer go-git's Remote needs.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/sideband"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Remote is where to fetch from.
type Remote struct {
	Endpoint  *transport.Endpoint
	Auth      transport.AuthMethod // nil: anonymous
	Transport transport.Transport
	// HTTPClient is the guarded client of http(s) remotes (nil over SSH).
	HTTPClient *http.Client
}

// Result is what a fetch changed.
type Result struct {
	Updated []string // refs created or moved
	Deleted []string // refs removed (prune)
	// DefaultBranch is the remote's HEAD branch, if it says.
	DefaultBranch string
}

// ErrEmptyRemote: the remote has no branches.
var ErrEmptyRemote = errors.New("the remote repository has no branches")

// mirrored reports whether a ref is copied: branches and tags.
func mirrored(name plumbing.ReferenceName) bool { return name.IsBranch() || name.IsTag() }

// FetchOptions say what to fetch besides branches and tags.
type FetchOptions struct {
	// Prune (mirrors): delete local branches and tags the remote doesn't
	// have; other refs (refs/pull/...) are never touched.
	Prune bool
	// Extra: other remote refs to copy under the same name (an import's
	// pull request heads); never pruned.
	Extra func(ref string) bool
}

// Fetch fetches the remote's branches and tags into st. HEAD follows the
// remote's default branch.
func Fetch(ctx context.Context, st storer.Storer, r Remote, o FetchOptions) (*Result, error) {
	return fetch(ctx, st, r, o)
}

func fetch(ctx context.Context, st storer.Storer, r Remote, o FetchOptions) (*Result, error) {
	prune := o.Prune
	sess, err := r.Transport.NewUploadPackSession(r.Endpoint, r.Auth)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return nil, ErrEmptyRemote
	} else if err != nil {
		return nil, err
	}
	all, err := adv.AllReferences()
	if err != nil {
		return nil, err
	}
	remote := map[plumbing.ReferenceName]plumbing.Hash{}
	extra := map[plumbing.ReferenceName]plumbing.Hash{}
	for name, ref := range all {
		switch {
		case ref.Type() != plumbing.HashReference:
		case mirrored(name):
			remote[name] = ref.Hash()
		case o.Extra != nil && o.Extra(name.String()):
			extra[name] = ref.Hash()
		}
	}
	if len(remote) == 0 {
		return nil, ErrEmptyRemote
	}

	// Wants: what we lack; haves: our branches and tags.
	var wants, haves []plumbing.Hash
	seen := map[plumbing.Hash]bool{}
	for _, h := range mergeMaps(remote, extra) {
		if !seen[h] && st.HasEncodedObject(h) != nil {
			seen[h] = true
			wants = append(wants, h)
		}
	}
	local := map[plumbing.ReferenceName]plumbing.Hash{}
	iter, err := st.IterReferences()
	if err != nil {
		return nil, err
	}
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if mirrored(ref.Name()) && ref.Type() == plumbing.HashReference {
			local[ref.Name()] = ref.Hash()
			haves = append(haves, ref.Hash())
		}
		return nil
	})
	if len(wants) > 0 {
		req := packp.NewUploadPackRequestFromCapabilities(adv.Capabilities)
		// No thin packs: the storers can't resolve deltas against objects
		// outside the pack.
		req.Capabilities.Delete(capability.ThinPack)
		req.Wants, req.Haves = wants, haves
		resp, err := sess.UploadPack(ctx, req)
		if err != nil {
			return nil, err
		}
		err = packfile.UpdateObjectStorage(st, sidebandReader(req.Capabilities, resp))
		resp.Close()
		if err != nil {
			return nil, fmt.Errorf("storing the fetched objects: %w", err)
		}
	}

	res := &Result{}
	for name, h := range remote {
		if old, ok := local[name]; ok && old == h {
			continue
		}
		if err := st.SetReference(plumbing.NewHashReference(name, h)); err != nil {
			return nil, err
		}
		res.Updated = append(res.Updated, name.String())
	}
	for name, h := range extra {
		if cur, err := st.Reference(name); err == nil && cur.Hash() == h {
			continue
		}
		if err := st.SetReference(plumbing.NewHashReference(name, h)); err != nil {
			return nil, err
		}
		res.Updated = append(res.Updated, name.String())
	}
	if prune {
		for name := range local {
			if _, ok := remote[name]; !ok {
				if err := st.RemoveReference(name); err != nil {
					return nil, err
				}
				res.Deleted = append(res.Deleted, name.String())
			}
		}
	}

	// HEAD: the remote's default branch.
	if target, ok := headTarget(adv); ok {
		if _, has := remote[target]; has {
			res.DefaultBranch = target.Short()
			if cur, err := st.Reference(plumbing.HEAD); err != nil || cur.Target() != target {
				if err := st.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, target)); err != nil {
					return nil, err
				}
			}
		}
	}
	return res, nil
}

func mergeMaps(a, b map[plumbing.ReferenceName]plumbing.Hash) map[plumbing.ReferenceName]plumbing.Hash {
	out := make(map[plumbing.ReferenceName]plumbing.Hash, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// headTarget is the branch the remote's HEAD points to: from the symref
// capability, else the branch at HEAD's commit.
func headTarget(adv *packp.AdvRefs) (plumbing.ReferenceName, bool) {
	for _, v := range adv.Capabilities.Get(capability.SymRef) {
		if from, to, ok := strings.Cut(v, ":"); ok && from == "HEAD" {
			return plumbing.ReferenceName(to), true
		}
	}
	if adv.Head == nil {
		return "", false
	}
	for name, h := range adv.References {
		if h == *adv.Head && strings.HasPrefix(name, "refs/heads/") {
			return plumbing.ReferenceName(name), true
		}
	}
	return "", false
}

func sidebandReader(l *capability.List, r io.Reader) io.Reader {
	switch {
	case l.Supports(capability.Sideband64k):
		return sideband.NewDemuxer(sideband.Sideband64k, r)
	case l.Supports(capability.Sideband):
		return sideband.NewDemuxer(sideband.Sideband, r)
	}
	return r
}
