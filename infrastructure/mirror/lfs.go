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

// LFSServer finds the remote's LFS API: <url>.git/info/lfs over http(s),
// git-lfs-authenticate over SSH (which also hands out credentials).
func (d Dialer) LFSServer(ctx context.Context, raw string, cred Credentials) (*LFSServer, error) {
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
	out, err := session.Output("git-lfs-authenticate '" + strings.Trim(ep.Path, "/") + "' download")
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate: %w", err)
	}
	var s LFSServer
	if err := json.Unmarshal(out, &s); err != nil || s.Href == "" {
		return nil, errors.New("git-lfs-authenticate: unexpected answer")
	}
	return &s, nil
}

// FetchLFS downloads objects from the LFS server, in batches, handing each
// to store (which verifies it).
func (d Dialer) FetchLFS(ctx context.Context, server *LFSServer, objects []LFSObject, store func(o LFSObject, r io.Reader) error) error {
	client := d.HTTPClient()
	for start := 0; start < len(objects); start += 100 {
		batch := objects[start:min(start+100, len(objects))]
		body, _ := json.Marshal(map[string]any{"operation": "download", "transfers": []string{"basic"}, "objects": batch})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(server.Href, "/")+"/objects/batch", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/vnd.git-lfs+json")
		req.Header.Set("Content-Type", "application/vnd.git-lfs+json")
		for k, v := range server.Header {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		var answer struct {
			Objects []struct {
				LFSObject
				Actions struct {
					Download *struct {
						Href   string            `json:"href"`
						Header map[string]string `json:"header"`
					} `json:"download"`
				} `json:"actions"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"objects"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&answer)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("LFS batch: %s", resp.Status)
		}
		if err != nil {
			return fmt.Errorf("LFS batch: %w", err)
		}
		for _, o := range answer.Objects {
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
