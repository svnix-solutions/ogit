package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/labbs/ogit/infrastructure/gitbrowse"
	"golang.org/x/crypto/ssh"
)

// LFSObject is a Git LFS object to fetch.
type LFSObject struct {
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
}

// maxPointerScan bounds the files looked at per fetch.
const maxPointerScan = 200000

// Pointers lists the LFS objects the trees of tips point to (each object
// once), files beyond maxPointerScan ignored.
func Pointers(st storer.Storer, tips []plumbing.Hash) ([]LFSObject, error) {
	seenBlob := map[plumbing.Hash]bool{}
	seenOid := map[string]bool{}
	var out []LFSObject
	scanned := 0
	for _, tip := range tips {
		c, err := object.GetCommit(st, tip)
		if err != nil {
			continue // a tag of a tree or blob: nothing to scan
		}
		tree, err := c.Tree()
		if err != nil {
			return nil, err
		}
		files := tree.Files()
		err = files.ForEach(func(f *object.File) error {
			if scanned++; scanned > maxPointerScan {
				return storer.ErrStop
			}
			if f.Mode == filemode.Submodule || f.Size >= 1024 || seenBlob[f.Hash] {
				return nil
			}
			seenBlob[f.Hash] = true
			text, err := f.Contents()
			if err != nil {
				return nil
			}
			if p, ok := gitbrowse.ParsePointer([]byte(text)); ok && !seenOid[p.Oid] {
				seenOid[p.Oid] = true
				out = append(out, LFSObject{Oid: p.Oid, Size: p.Size})
			}
			return nil
		})
		files.Close()
		if err != nil && !errors.Is(err, storer.ErrStop) {
			return nil, err
		}
	}
	return out, nil
}

// LFSServer is a remote's LFS API.
type LFSServer struct {
	Href   string // .../info/lfs
	Header map[string]string
}

// LFSServer finds the remote's LFS API for an operation (download or
// upload): <url>.git/info/lfs over http(s), git-lfs-authenticate over SSH
// (which also hands out credentials).
func (d Dialer) LFSServer(ctx context.Context, raw string, cred Credentials, operation string) (*LFSServer, error) {
	ep, err := transport.NewEndpoint(strings.TrimSpace(raw))
	if err != nil {
		return nil, ErrURL
	}
	if ep.Protocol != "ssh" {
		href := strings.TrimSuffix(strings.TrimSpace(raw), "/")
		if !strings.HasSuffix(href, ".git") {
			href += ".git"
		}
		s := &LFSServer{Href: href + "/info/lfs", Header: map[string]string{}}
		if cred.Username != "" || cred.Password != "" {
			user := cred.Username
			if user == "" {
				user = "git"
			}
			req, _ := http.NewRequest(http.MethodGet, href, nil)
			req.SetBasicAuth(user, cred.Password)
			s.Header["Authorization"] = req.Header.Get("Authorization")
		}
		return s, nil
	}
	auth, err := d.sshAuth(ctx, ep, cred)
	if err != nil {
		return nil, err
	}
	config, err := auth.ClientConfig()
	if err != nil {
		return nil, err
	}
	config.Timeout = d.timeout()
	port := ep.Port
	if port == 0 {
		port = 22
	}
	client, err := ssh.Dial("tcp", net.JoinHostPort(ep.Host, strconv.Itoa(port)), config)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	out, err := session.Output("git-lfs-authenticate '" + strings.Trim(ep.Path, "/") + "' " + operation)
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate: %w", err)
	}
	var s LFSServer
	if err := json.Unmarshal(out, &s); err != nil || s.Href == "" {
		return nil, errors.New("git-lfs-authenticate: unexpected answer")
	}
	return &s, nil
}

// lfsAction is a batch answer's download, upload or verify action.
type lfsAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type lfsAnswer struct {
	LFSObject
	Actions struct {
		Download *lfsAction `json:"download"`
		Upload   *lfsAction `json:"upload"`
		Verify   *lfsAction `json:"verify"`
	} `json:"actions"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// batch asks the LFS server about objects (at most 100) for an operation.
func (d Dialer) batch(ctx context.Context, client *http.Client, server *LFSServer, operation string, objects []LFSObject) ([]lfsAnswer, error) {
	body, _ := json.Marshal(map[string]any{"operation": operation, "transfers": []string{"basic"}, "objects": objects})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(server.Href, "/")+"/objects/batch", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	for k, v := range server.Header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	var answer struct {
		Objects []lfsAnswer `json:"objects"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&answer)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LFS batch: %s", resp.Status)
	}
	if err != nil {
		return nil, fmt.Errorf("LFS batch: %w", err)
	}
	return answer.Objects, nil
}

// FetchLFS downloads objects from the LFS server, in batches, handing each
// to store (which verifies it).
func (d Dialer) FetchLFS(ctx context.Context, server *LFSServer, objects []LFSObject, store func(o LFSObject, r io.Reader) error) error {
	client := d.HTTPClient()
	for start := 0; start < len(objects); start += 100 {
		answers, err := d.batch(ctx, client, server, "download", objects[start:min(start+100, len(objects))])
		if err != nil {
			return err
		}
		for _, o := range answers {
			if o.Error != nil || o.Actions.Download == nil {
				continue // missing on the remote too: the pointer stays a pointer
			}
			if err := d.download(ctx, client, o.Actions.Download.Href, o.Actions.Download.Header, o.LFSObject, store); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d Dialer) download(ctx context.Context, client *http.Client, href string, header map[string]string, o LFSObject, store func(LFSObject, io.Reader) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	if err != nil {
		return err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LFS download of %s: %s", o.Oid, resp.Status)
	}
	return store(o, io.LimitReader(resp.Body, o.Size+1))
}

// PushLFS uploads the objects the LFS server lacks, in batches; open
// returns an object's content (nil, nil: we don't have it, skipped).
func (d Dialer) PushLFS(ctx context.Context, server *LFSServer, objects []LFSObject, open func(o LFSObject) (io.ReadCloser, error)) error {
	client := d.HTTPClient()
	for start := 0; start < len(objects); start += 100 {
		answers, err := d.batch(ctx, client, server, "upload", objects[start:min(start+100, len(objects))])
		if err != nil {
			return err
		}
		for _, o := range answers {
			if o.Error != nil {
				return fmt.Errorf("LFS upload of %s: %s", o.Oid, o.Error.Message)
			}
			if o.Actions.Upload == nil {
				continue // the server has it
			}
			body, err := open(o.LFSObject)
			if err != nil {
				return err
			}
			if body == nil {
				continue
			}
			err = d.send(ctx, client, http.MethodPut, o.Actions.Upload, "application/octet-stream", body, o.Size)
			body.Close()
			if err != nil {
				return fmt.Errorf("LFS upload of %s: %w", o.Oid, err)
			}
			if o.Actions.Verify != nil {
				v, _ := json.Marshal(o.LFSObject)
				if err := d.send(ctx, client, http.MethodPost, o.Actions.Verify, "application/vnd.git-lfs+json", io.NopCloser(bytes.NewReader(v)), int64(len(v))); err != nil {
					return fmt.Errorf("LFS verify of %s: %w", o.Oid, err)
				}
			}
		}
	}
	return nil
}

func (d Dialer) send(ctx context.Context, client *http.Client, method string, a *lfsAction, contentType string, body io.ReadCloser, size int64) error {
	req, err := http.NewRequestWithContext(ctx, method, a.Href, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	if contentType != "application/octet-stream" {
		req.Header.Set("Accept", contentType)
	}
	for k, v := range a.Header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errors.New(resp.Status)
	}
	return nil
}
