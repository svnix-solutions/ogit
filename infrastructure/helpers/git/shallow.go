package git

import (
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Shallow fetches (git clone/fetch --depth N, and actions/checkout, which
// fetches with --depth=1). go-git's server refuses them, so they are
// served here:
//
//   - "deepen N" keeps the commits less than N steps from a want (the wants
//     are at depth 1). The commits at depth N that have parents are the new
//     shallow boundary, sent as "shallow <id>"; the client's own shallow
//     commits that are now inside the depth are sent as "unshallow <id>".
//     The list ends with a flush-pkt and comes before the negotiation, in
//     every response over HTTP (the client resends its request each round).
//   - The client's "shallow <id>" lines (a shallow repository fetching,
//     with or without deepen) are commits whose parents it lacks: the
//     objects it has are walked from its haves down to them, never past.
//   - The pack has what the wants reach within the depth, minus what the
//     haves reach. deepen-since and deepen-not are not advertised.

var errDeepenUnsupported = errors.New("only --depth is supported for shallow fetches (not --shallow-since or --shallow-exclude)")

// shallowPlan is the boundary of a shallow fetch.
type shallowPlan struct {
	depth     int
	commits   []plumbing.Hash // the commits to send, wants first
	shallow   []plumbing.Hash // new boundary commits (not already shallow on the client)
	unshallow []plumbing.Hash // the client's shallow commits now inside the depth
	boundary  map[plumbing.Hash]bool
}

// isShallowRequest reports whether the request needs the shallow path.
func isShallowRequest(req *packp.UploadRequest) bool {
	return len(req.Shallows) > 0 || req.Depth != nil && !req.Depth.IsZero()
}

// planShallow computes the commits within the requested depth. Without a
// depth (a shallow client fetching normally), every commit reachable from
// the wants is kept; the haves' walk stops at the client's shallow commits.
func planShallow(objects storer.EncodedObjectStorer, req *packp.UploadRequest) (*shallowPlan, error) {
	depth := 0 // unlimited
	switch d := req.Depth.(type) {
	case nil:
	case packp.DepthCommits:
		depth = int(d)
	default:
		if !req.Depth.IsZero() {
			return nil, errDeepenUnsupported
		}
	}
	clientShallow := map[plumbing.Hash]bool{}
	for _, h := range req.Shallows {
		clientShallow[h] = true
	}
	p := &shallowPlan{depth: depth, boundary: map[plumbing.Hash]bool{}}
	seen := map[plumbing.Hash]bool{}
	type item struct {
		hash  plumbing.Hash
		depth int
	}
	queue := []item{}
	for _, w := range req.Wants {
		w, err := peelToCommit(objects, w)
		if err != nil {
			return nil, err
		}
		if !w.IsZero() && !seen[w] {
			seen[w] = true
			queue = append(queue, item{w, 1})
		}
	}
	for len(queue) > 0 { // breadth first: each commit at its smallest depth
		it := queue[0]
		queue = queue[1:]
		c, err := object.GetCommit(objects, it.hash)
		if err != nil {
			return nil, err
		}
		p.commits = append(p.commits, c.Hash)
		if depth > 0 && it.depth >= depth {
			if len(c.ParentHashes) > 0 {
				p.boundary[c.Hash] = true
				if !clientShallow[c.Hash] {
					p.shallow = append(p.shallow, c.Hash)
				}
			}
			continue
		}
		if clientShallow[c.Hash] && depth > 0 {
			p.unshallow = append(p.unshallow, c.Hash)
		}
		if clientShallow[c.Hash] && depth == 0 {
			continue // the client has it, and lacks what's below: nothing to walk
		}
		for _, parent := range c.ParentHashes {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, item{parent, it.depth + 1})
			}
		}
	}
	return p, nil
}

// peelToCommit follows annotated tags to the commit they point at; zero
// for a want that leads to no commit (a tagged tree or blob).
func peelToCommit(objects storer.EncodedObjectStorer, h plumbing.Hash) (plumbing.Hash, error) {
	for i := 0; i < 10; i++ {
		o, err := objects.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		switch o.Type() {
		case plumbing.CommitObject:
			return h, nil
		case plumbing.TagObject:
			t, err := object.DecodeTag(objects, o)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			h = t.Target
		default:
			return plumbing.ZeroHash, nil
		}
	}
	return plumbing.ZeroHash, nil
}

// writeShallowUpdate sends the shallow and unshallow lines and a flush.
func writeShallowUpdate(out io.Writer, p *shallowPlan) error {
	enc := pktline.NewEncoder(out)
	for _, h := range p.shallow {
		if err := enc.Encodef("shallow %s\n", h); err != nil {
			return err
		}
	}
	for _, h := range p.unshallow {
		if err := enc.Encodef("unshallow %s\n", h); err != nil {
			return err
		}
	}
	return enc.Flush()
}

// writeShallowPack sends the plan's commits and what they reference, minus
// what the client has: the objects reachable from its haves, walked down to
// its shallow commits and no further.
func writeShallowPack(objects storer.EncodedObjectStorer, req *packp.UploadRequest, p *shallowPlan, haves []plumbing.Hash, out io.Writer) error {
	clientShallow := map[plumbing.Hash]bool{}
	for _, h := range req.Shallows {
		clientShallow[h] = true
	}
	has := map[plumbing.Hash]bool{}
	commits := map[plumbing.Hash]bool{}
	queue := append([]plumbing.Hash(nil), haves...)
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if commits[h] {
			continue
		}
		commits[h] = true
		c, err := object.GetCommit(objects, h)
		if err != nil {
			continue // not a commit we know
		}
		if err := addTree(objects, c.TreeHash, has, nil); err != nil {
			return err
		}
		has[c.Hash] = true
		if clientShallow[c.Hash] {
			continue
		}
		queue = append(queue, c.ParentHashes...)
	}

	var send []plumbing.Hash
	added := map[plumbing.Hash]bool{}
	add := func(h plumbing.Hash) {
		if !has[h] && !added[h] {
			added[h] = true
			send = append(send, h)
		}
	}
	for _, w := range req.Wants { // annotated tags and anything else wanted
		if o, err := objects.EncodedObject(plumbing.AnyObject, w); err == nil && o.Type() == plumbing.TagObject {
			add(w)
		}
	}
	for _, h := range p.commits {
		if has[h] {
			continue
		}
		c, err := object.GetCommit(objects, h)
		if err != nil {
			return err
		}
		add(c.Hash)
		if err := addTree(objects, c.TreeHash, has, add); err != nil {
			return err
		}
	}
	if _, err := packfile.NewEncoder(out, objects, false).Encode(send, 10); err != nil {
		return fmt.Errorf("write pack: %w", err)
	}
	return nil
}

// addTree marks a tree and everything under it: into into (when add is
// nil), or through add, skipping what into already has.
func addTree(objects storer.EncodedObjectStorer, h plumbing.Hash, into map[plumbing.Hash]bool, add func(plumbing.Hash)) error {
	if into[h] && add != nil {
		return nil
	}
	if add == nil {
		if into[h] {
			return nil
		}
		into[h] = true
	} else {
		add(h)
		into[h] = true // walked once
	}
	t, err := object.GetTree(objects, h)
	if err != nil {
		return err
	}
	for _, e := range t.Entries {
		switch e.Mode {
		case filemode.Dir:
			if err := addTree(objects, e.Hash, into, add); err != nil {
				return err
			}
		case filemode.Submodule:
			// a commit in another repository
		default:
			if !into[e.Hash] {
				if add != nil {
					add(e.Hash)
				}
				into[e.Hash] = true
			}
		}
	}
	return nil
}
