package mirror

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// PushOptions say how to push.
type PushOptions struct {
	// Force makes the remote's branches and tags identical to ours:
	// forced updates, and deletion of the ones we don't have. Otherwise
	// diverged branches and moved tags are rejected and the remote's other
	// refs kept.
	Force bool
	// BeforeSend, if set, gets the tips about to be pushed (to upload
	// their LFS objects first); an error stops the push.
	BeforeSend func(tips []plumbing.Hash) error
}

// ErrNothingToPush: the repository has no branches (a forced push would
// empty the remote).
var ErrNothingToPush = errors.New("the repository has no branches to push")

// PushResult is what a push did.
type PushResult struct {
	Updated  []string          // refs created, moved or deleted on the remote
	Rejected map[string]string // ref -> why it wasn't pushed
}

// Err sums up the rejected refs, if any.
func (r *PushResult) Err() error {
	if len(r.Rejected) == 0 {
		return nil
	}
	names := make([]string, 0, len(r.Rejected))
	for name := range r.Rejected {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = plumbing.ReferenceName(name).Short() + ": " + r.Rejected[name]
	}
	return errors.New("not pushed: " + strings.Join(parts, "; "))
}

// Push pushes st's branches and tags to the remote. Refs are independent:
// one rejected (here or by the remote) doesn't stop the others.
func Push(ctx context.Context, st storer.Storer, r Remote, o PushOptions) (*PushResult, error) {
	local := map[plumbing.ReferenceName]plumbing.Hash{}
	iter, err := st.IterReferences()
	if err != nil {
		return nil, err
	}
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if mirrored(ref.Name()) && ref.Type() == plumbing.HashReference {
			local[ref.Name()] = ref.Hash()
		}
		return nil
	})

	hasBranch := false
	for name := range local {
		hasBranch = hasBranch || name.IsBranch()
	}
	if !hasBranch {
		return nil, ErrNothingToPush
	}

	sess, err := r.Transport.NewReceivePackSession(r.Endpoint, r.Auth)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return nil, err
	}
	remote := map[plumbing.ReferenceName]plumbing.Hash{}
	var all []plumbing.Hash // every remote tip: what it has
	if adv != nil {
		refs, err := adv.AllReferences()
		if err != nil {
			return nil, err
		}
		for name, ref := range refs {
			if ref.Type() != plumbing.HashReference {
				continue
			}
			all = append(all, ref.Hash())
			if mirrored(name) {
				remote[name] = ref.Hash()
			}
		}
	}
	caps := capability.NewList()
	if adv != nil {
		caps = adv.Capabilities
	}

	res := &PushResult{Rejected: map[string]string{}}
	req := packp.NewReferenceUpdateRequestFromCapabilities(caps)
	for name, h := range local {
		old, ok := remote[name]
		switch {
		case ok && old == h:
			continue
		case !ok, o.Force:
		case name.IsTag():
			res.Rejected[name.String()] = "the tag points elsewhere on the remote"
			continue
		case !fastForward(st, old, h):
			res.Rejected[name.String()] = "diverged from the remote (not a fast-forward)"
			continue
		}
		req.Commands = append(req.Commands, &packp.Command{Name: name, Old: old, New: h})
	}
	if o.Force {
		for name, old := range remote {
			if _, ok := local[name]; ok {
				continue
			}
			if !caps.Supports(capability.DeleteRefs) {
				res.Rejected[name.String()] = "the remote doesn't allow deleting refs"
				continue
			}
			req.Commands = append(req.Commands, &packp.Command{Name: name, Old: old, New: plumbing.ZeroHash})
		}
	}
	if len(req.Commands) == 0 {
		return res, nil
	}
	sort.Slice(req.Commands, func(i, j int) bool { return req.Commands[i].Name < req.Commands[j].Name })

	var wants []plumbing.Hash
	for _, cmd := range req.Commands {
		if !cmd.New.IsZero() {
			wants = append(wants, cmd.New)
		}
	}
	if len(wants) > 0 {
		if o.BeforeSend != nil {
			if err := o.BeforeSend(wants); err != nil {
				return nil, err
			}
		}
		var haves []plumbing.Hash // only what we have: revlist walks them
		for _, h := range all {
			if st.HasEncodedObject(h) == nil {
				haves = append(haves, h)
			}
		}
		hashes, err := revlist.Objects(st, wants, haves)
		if err != nil {
			return nil, fmt.Errorf("listing the objects to push: %w", err)
		}
		// A complete pack (deltas only within it): remotes, oGit included,
		// may refuse thin ones.
		pr, pw := io.Pipe()
		defer pr.Close()
		go func() {
			_, err := packfile.NewEncoder(pw, st, !caps.Supports(capability.OFSDelta)).Encode(hashes, 10)
			pw.CloseWithError(err)
		}()
		req.Packfile = pr
	}

	var report *packp.ReportStatus
	if r.HTTPClient != nil {
		report, err = receivePackHTTP(ctx, r, req)
	} else {
		report, err = sess.ReceivePack(ctx, req)
	}
	if report != nil {
		if report.UnpackStatus != "" && report.UnpackStatus != "ok" {
			return nil, fmt.Errorf("the remote couldn't unpack: %s", report.UnpackStatus)
		}
		refused := map[plumbing.ReferenceName]string{}
		for _, s := range report.CommandStatuses {
			if s.Status != "ok" {
				refused[s.ReferenceName] = strings.TrimPrefix(s.Status, "ng ")
			}
		}
		for _, cmd := range req.Commands {
			if why, ok := refused[cmd.Name]; ok {
				res.Rejected[cmd.Name.String()] = "refused by the remote: " + why
			} else {
				res.Updated = append(res.Updated, cmd.Name.String())
			}
		}
		return res, nil
	}
	if err != nil {
		return nil, err
	}
	for _, cmd := range req.Commands { // no report-status: all or nothing
		res.Updated = append(res.Updated, cmd.Name.String())
	}
	return res, nil
}

// fastForward reports whether new descends from old (both commits we
// have).
func fastForward(st storer.EncodedObjectStorer, old, new plumbing.Hash) bool {
	o, err := object.GetCommit(st, old)
	if err != nil {
		return false
	}
	n, err := object.GetCommit(st, new)
	if err != nil {
		return false
	}
	ok, err := o.IsAncestor(n)
	return err == nil && ok
}

// postBuffer is the largest request sent with a Content-Length; larger
// ones are chunked (git's http.postBuffer: some servers, git http-backend
// behind CGI for one, only take chunked bodies for large pushes).
const postBuffer = 1 << 20

// receivePackHTTP sends the request streaming: go-git's HTTP transport
// would hold the whole pack in memory.
func receivePackHTTP(ctx context.Context, r Remote, req *packp.ReferenceUpdateRequest) (*packp.ReportStatus, error) {
	pr, w := io.Pipe()
	go func() { w.CloseWithError(req.Encode(w)) }()
	defer pr.Close()
	head := make([]byte, postBuffer)
	n, err := io.ReadFull(pr, head)
	var body io.Reader
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		body = bytes.NewReader(head[:n]) // all of it: a Content-Length
	case err != nil:
		return nil, err
	default:
		body = io.MultiReader(bytes.NewReader(head), pr)
	}
	// r.Endpoint followed the advertisement's redirects.
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.Endpoint.String()+"/"+transport.ReceivePackServiceName, body)
	if err != nil {
		return nil, err
	}
	if _, whole := body.(*bytes.Reader); !whole {
		hreq.ContentLength = -1
	}
	hreq.Header.Set("User-Agent", capability.DefaultAgent())
	hreq.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	hreq.Header.Set("Accept", "application/x-git-receive-pack-result")
	if a, ok := r.Auth.(githttp.AuthMethod); ok {
		a.SetAuth(hreq)
	}
	resp, err := r.HTTPClient.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, transport.ErrAuthenticationRequired
	case resp.StatusCode == http.StatusForbidden:
		return nil, transport.ErrAuthorizationFailed
	case resp.StatusCode == http.StatusNotFound:
		return nil, transport.ErrRepositoryNotFound
	case resp.StatusCode/100 == 3:
		return nil, fmt.Errorf("the remote redirected the push to %s: use that URL", resp.Header.Get("Location"))
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the remote answered %s", resp.Status)
	}
	if !req.Capabilities.Supports(capability.ReportStatus) {
		return nil, nil
	}
	report := packp.NewReportStatus()
	if err := report.Decode(resp.Body); err != nil {
		return nil, fmt.Errorf("reading the remote's report: %w", err)
	}
	return report, nil
}
