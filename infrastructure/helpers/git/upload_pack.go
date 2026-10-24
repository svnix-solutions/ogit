package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Fetch negotiation. go-git's server does none: it expects every "have" up
// front and always answers with a pack. That breaks as soon as the client
// needs a second round (more than 32 commits): the client waits for an
// ACK/NAK and receives pack data instead. It also fails on haves it does not
// know (the client's unpushed commits). ServeUploadPack implements the
// negotiation itself and only uses go-git to build the pack.
//
// Two modes, matching the two transports:
//
//   - SSH (stateful, one stream, plain protocol as advertised by go-git):
//     "ACK <id>" once, for the first common have; "NAK" on each flush until
//     then; "NAK" after "done" if nothing was common; then the pack.
//
//   - HTTP (stateless, every round is a separate request): the server cannot
//     remember commons, and a plain-mode client does not resend them, so
//     HTTP advertises multi_ack_detailed (AdvertiseUploadPackHTTP). Commons
//     are answered "ACK <id> common" and the client resends them in every
//     later request; at the flush after the first common the server says
//     "ACK <id> ready" and the client sends "done". Each round ends with
//     "NAK"; after "done" the server sends "ACK <last common>" (or "NAK")
//     and the pack.
//
// "ready" is sent as soon as one have is common. For a fetch of one branch
// that is the newest shared commit (clients send haves newest first); with
// several wants the pack may contain some objects the client has.

// AdvertiseUploadPackHTTP adds multi_ack_detailed to an upload-pack
// advertisement served over HTTP.
func AdvertiseUploadPackHTTP(adv *packp.AdvRefs) error {
	return adv.Capabilities.Set(capability.MultiACKDetailed)
}

// ServeUploadPack handles a fetch after the ref advertisement. stateless is
// true for HTTP (see above); a stateless request without "done" only gets the
// negotiation replies.
func ServeUploadPack(ctx context.Context, sess transport.UploadPackSession, objects storer.EncodedObjectStorer, in io.Reader, out io.Writer, stateless bool) error {
	wants := packp.NewUploadRequest()
	if err := wants.Decode(in); err != nil {
		return fmt.Errorf("read wants: %w", err)
	}
	if len(wants.Shallows) > 0 || wants.Depth != nil && !wants.Depth.IsZero() {
		return errors.New("shallow clones are not supported")
	}
	detailed := stateless && wants.Capabilities.Supports(capability.MultiACKDetailed)
	// go-git rejects capabilities it does not implement itself.
	wants.Capabilities.Delete(capability.MultiACKDetailed)

	enc := pktline.NewEncoder(out)
	scanner := pktline.NewScanner(in)
	var common []plumbing.Hash
	acked, ready := false, false

	for {
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read haves: %w", err)
			}
			return nil // stateless round without "done"
		}
		line := strings.TrimSuffix(string(scanner.Bytes()), "\n")
		switch {
		case strings.HasPrefix(line, "have "):
			h := plumbing.NewHash(strings.TrimPrefix(line, "have "))
			if objects.HasEncodedObject(h) != nil {
				continue // the client's own commits: not common
			}
			common = append(common, h)
			var err error
			switch {
			case detailed:
				err = enc.Encodef("ACK %s common\n", h)
			case !acked:
				acked = true
				err = enc.Encodef("ACK %s\n", h)
			}
			if err != nil {
				return err
			}

		case line == "": // flush-pkt: end of a round
			var err error
			switch {
			case detailed:
				if len(common) > 0 && !ready {
					ready = true
					if err = enc.Encodef("ACK %s ready\n", common[len(common)-1]); err != nil {
						return err
					}
				}
				err = enc.Encodef("NAK\n")
			case !acked:
				err = enc.Encodef("NAK\n")
			}
			if err != nil {
				return err
			}

		case line == "done":
			var err error
			switch {
			case detailed && len(common) > 0:
				err = enc.Encodef("ACK %s\n", common[len(common)-1])
			case acked: // plain: the ACK went out during negotiation
			default:
				err = enc.Encodef("NAK\n")
			}
			if err != nil {
				return err
			}
			return writePack(ctx, sess, wants, common, out)

		default:
			return fmt.Errorf("unexpected line in negotiation: %q", line)
		}
	}
}

// writePack sends the objects reachable from the wants but not from common.
func writePack(ctx context.Context, sess transport.UploadPackSession, wants *packp.UploadRequest, common []plumbing.Hash, out io.Writer) error {
	req := packp.NewUploadPackRequest()
	req.UploadRequest = *wants
	req.Haves = common
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return err
	}
	defer resp.Close()
	// Only the pack: the negotiation replies are already written (and
	// resp.Encode would add its own NAK).
	_, err = io.Copy(out, resp)
	return err
}
