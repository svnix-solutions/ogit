package git

import (
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage"
)

// Pushes must update refs with compare-and-swap. go-git's receive-pack only
// checks that a ref exists before overwriting it and ignores the old value
// the client sent, so two concurrent pushes (or two servers sharing one
// bucket) can silently lose one of them. GuardedReceivePack wraps the storer
// go-git writes through so that every ref write it makes checks the client's
// expected old value.

// ReferenceCreator is implemented by storers that can create a reference
// only if it does not exist (S3: If-None-Match). Others fall back to
// go-git's existence check, which leaves a small race for concurrent creates.
type ReferenceCreator interface {
	CreateReference(ref *plumbing.Reference) error
}

// ConditionalReferenceRemover is implemented by storers that can delete a
// reference only if it still has the expected value.
type ConditionalReferenceRemover interface {
	RemoveReferenceIf(name plumbing.ReferenceName, old plumbing.Hash) error
}

// GuardedReceivePack returns a receive-pack session over st whose ref
// updates are checked against the request, and a function that must be
// called with the decoded request before ReceivePack.
func GuardedReceivePack(st storer.Storer, ep *transport.Endpoint) (transport.ReceivePackSession, func(*packp.ReferenceUpdateRequest), error) {
	g := &guardedStorer{Storer: st, expected: map[plumbing.ReferenceName]plumbing.Hash{}}
	var wrapped storer.Storer = g
	// go-git detects PackfileWriter with a type assertion; keep it visible.
	if pw, ok := st.(storer.PackfileWriter); ok {
		wrapped = &guardedPackStorer{guardedStorer: g, pw: pw}
	}
	sess, err := server.NewServer(staticLoader{wrapped}).NewReceivePackSession(ep, nil)
	if err != nil {
		return nil, nil, err
	}
	expect := func(req *packp.ReferenceUpdateRequest) {
		for _, cmd := range req.Commands {
			g.expected[cmd.Name] = cmd.Old
		}
	}
	return sess, expect, nil
}

type staticLoader struct{ st storer.Storer }

func (l staticLoader) Load(*transport.Endpoint) (storer.Storer, error) { return l.st, nil }

type guardedStorer struct {
	storer.Storer
	expected map[plumbing.ReferenceName]plumbing.Hash
}

func (g *guardedStorer) SetReference(ref *plumbing.Reference) error {
	old, ok := g.expected[ref.Name()]
	if !ok {
		return g.Storer.SetReference(ref)
	}
	if old.IsZero() { // create
		if c, ok := g.Storer.(ReferenceCreator); ok {
			return c.CreateReference(ref)
		}
		return g.Storer.SetReference(ref)
	}
	return g.Storer.CheckAndSetReference(ref, plumbing.NewHashReference(ref.Name(), old))
}

func (g *guardedStorer) RemoveReference(name plumbing.ReferenceName) error {
	old, ok := g.expected[name]
	if !ok {
		return g.Storer.RemoveReference(name)
	}
	if r, ok := g.Storer.(ConditionalReferenceRemover); ok {
		return r.RemoveReferenceIf(name, old)
	}
	current, err := g.Storer.Reference(name)
	if err != nil {
		return err
	}
	if current.Hash() != old {
		return storage.ErrReferenceHasChanged
	}
	return g.Storer.RemoveReference(name)
}

type guardedPackStorer struct {
	*guardedStorer
	pw storer.PackfileWriter
}

func (g *guardedPackStorer) PackfileWriter() (io.WriteCloser, error) { return g.pw.PackfileWriter() }
