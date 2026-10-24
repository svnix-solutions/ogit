package ssh

import (
	"bufio"
	"context"
	"io"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
)

// serveUploadPack runs git-upload-pack (clone/fetch) over one SSH channel:
// advertise refs, negotiate, send the pack.
func serveUploadPack(ctx context.Context, srv transport.Transport, ep *transport.Endpoint, objects storer.EncodedObjectStorer, in *bufio.Reader, out io.Writer) error {
	sess, err := srv.NewUploadPackSession(ep, nil)
	if err != nil {
		return err
	}
	defer sess.Close()

	adv, err := sess.AdvertisedReferences()
	if err != nil {
		return err
	}
	if err := adv.Encode(out); err != nil {
		return err
	}

	if done, err := isFlush(in); err != nil || done {
		return err // nothing wanted (already up to date)
	}
	return helpergit.ServeUploadPack(ctx, sess, objects, in, out, false)
}

// serveReceivePack runs git-receive-pack (push) over one SSH channel.
func serveReceivePack(ctx context.Context, ep *transport.Endpoint, objects storer.Storer, in *bufio.Reader, out io.Writer) error {
	// Ref updates are checked against the old values in the request.
	sess, expect, err := helpergit.GuardedReceivePack(objects, ep)
	if err != nil {
		return err
	}
	defer sess.Close()

	adv, err := sess.AdvertisedReferences()
	if err != nil {
		return err
	}
	if err := helpergit.AdvertiseNoThin(adv); err != nil {
		return err
	}
	if err := adv.Encode(out); err != nil {
		return err
	}

	if done, err := isFlush(in); err != nil || done {
		return err // nothing to push
	}

	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(in); err != nil {
		return err
	}
	// The decoder always treats the rest of the stream as the packfile. A
	// push that only deletes refs sends none and then waits for the report,
	// so reading a pack would block forever (over HTTP the body just ends).
	if onlyDeletes(req) {
		req.Packfile = nil
	}
	expect(req)
	report, err := sess.ReceivePack(ctx, req)
	if report != nil {
		if encErr := report.Encode(out); encErr != nil && err == nil {
			err = encErr
		}
	}
	return err
}

func onlyDeletes(req *packp.ReferenceUpdateRequest) bool {
	for _, cmd := range req.Commands {
		if cmd.Action() != packp.Delete {
			return false
		}
	}
	return len(req.Commands) > 0
}
