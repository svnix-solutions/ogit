package git

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
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

// AdvertiseUploadPack adds what oGit serves beyond go-git to an upload-pack
// advertisement: shallow fetches (shallow.go), and wants of any commit a
// branch or tag reaches, not only their tips (actions/checkout fetches
// GITHUB_SHA, which may no longer be a tip; checkWants enforces "reaches").
func AdvertiseUploadPack(adv *packp.AdvRefs) error {
	for _, c := range []capability.Capability{capability.Shallow, capability.AllowTipSHA1InWant, capability.AllowReachableSHA1InWant} {
		if err := adv.Capabilities.Set(c); err != nil {
			return err
		}
	}
	return nil
}

// AdvertiseUploadPackHTTP is AdvertiseUploadPack plus multi_ack_detailed,
// for HTTP.
func AdvertiseUploadPackHTTP(adv *packp.AdvRefs) error {
	if err := AdvertiseUploadPack(adv); err != nil {
		return err
	}
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
	detailed := stateless && wants.Capabilities.Supports(capability.MultiACKDetailed)
	// go-git rejects capabilities it does not implement itself.
	wants.Capabilities.Delete(capability.MultiACKDetailed)
	wants.Capabilities.Delete(capability.Shallow)

	if err := checkWants(objects, wants.Wants); err != nil {
		return err
	}

	var plan *shallowPlan
	if isShallowRequest(wants) {
		var err error
		if plan, err = planShallow(objects, wants); err != nil {
			return err
		}
		// Only with a depth does the client wait for this list (shallow.go).
		if plan.depth > 0 {
			if err := writeShallowUpdate(out, plan); err != nil {
				return err
			}
		}
	}

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
			if plan != nil {
				return writeShallowPack(objects, wants, plan, common, out)
			}
			return writePack(ctx, sess, wants, common, out)

		default:
			return fmt.Errorf("unexpected line in negotiation: %q", line)
		}
	}
}

// checkWants refuses wants that no ref reaches: a client may ask for any
// commit of the history by its id, but not for objects left over from
// deleted or rewritten branches.
func checkWants(objects storer.EncodedObjectStorer, wants []plumbing.Hash) error {
	refs, ok := objects.(storer.ReferenceStorer)
	if !ok {
		return nil
	}
	need := map[plumbing.Hash]bool{}
	for _, w := range wants {
		need[w] = true
	}
	iter, err := refs.IterReferences()
	if err != nil {
		return err
	}
	var queue []plumbing.Hash
	err = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference {
			delete(need, r.Hash())
			queue = append(queue, r.Hash())
		}
		return nil
	})
	if err != nil || len(need) == 0 {
		return err
	}
	// Walk from every tip (tags peeled) until each want is found.
	seen := map[plumbing.Hash]bool{}
	for len(queue) > 0 && len(need) > 0 {
		h := queue[0]
		queue = queue[1:]
		if seen[h] {
			continue
		}
		seen[h] = true
		delete(need, h)
		o, err := objects.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			continue
		}
		switch o.Type() {
		case plumbing.TagObject:
			if t, err := object.DecodeTag(objects, o); err == nil {
				queue = append(queue, t.Target)
			}
		case plumbing.CommitObject:
			if c, err := object.DecodeCommit(objects, o); err == nil {
				queue = append(queue, c.ParentHashes...)
			}
		}
	}
	for w := range need {
		return fmt.Errorf("not our ref %s", w)
	}
	return nil
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
