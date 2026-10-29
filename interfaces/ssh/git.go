package ssh

import (
	"bufio"
	"context"
	"errors"
	"io"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labbs/ogit/application"
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
	if err := helpergit.AdvertiseUploadPack(adv); err != nil {
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
// It returns the reference updates that were applied.
func serveReceivePack(ctx context.Context, ep *transport.Endpoint, objects storer.Storer, in *bufio.Reader, out io.Writer, check helpergit.RefCheck, maxPush int64) ([]application.RefUpdate, error) {
	// Ref updates are checked against the old values in the request, and
	// against branch protection.
	sess, expect, err := helpergit.GuardedReceivePack(objects, ep, check)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	adv, err := sess.AdvertisedReferences()
	if err != nil {
		return nil, err
	}
	if err := helpergit.AdvertiseNoThin(adv); err != nil {
		return nil, err
	}
	if err := adv.Encode(out); err != nil {
		return nil, err
	}

	if done, err := isFlush(in); err != nil || done {
		return nil, err // nothing to push
	}

	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(in); err != nil {
		return nil, err
	}
	// The decoder always treats the rest of the stream as the packfile. A
	// push that only deletes refs sends none and then waits for the report,
	// so reading a pack would block forever (over HTTP the body just ends).
	if helpergit.OnlyDeletes(req) {
		req.Packfile = nil
	}
	if err := helpergit.SpoolPack(req, maxPush); errors.Is(err, helpergit.ErrPushTooLarge) {
		return nil, helpergit.RefusedReport(req, err).Encode(out)
	} else if err != nil {
		return nil, err
	}
	expect(req)
	report, err := sess.ReceivePack(ctx, req)
	if report != nil {
		if encErr := report.Encode(out); encErr != nil && err == nil {
			err = encErr
		}
	}
	return application.AppliedUpdates(req, report), err
}
