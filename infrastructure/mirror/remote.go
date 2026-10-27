package mirror

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/labbs/ogit/infrastructure/netguard"
	"golang.org/x/crypto/ssh"
)

var (
	// ErrURL: not an http(s) or ssh URL (scp-like git@host:path included),
	// or one with credentials in it.
	ErrURL = errors.New("the remote must be an https://, http://, ssh:// or git@host:path URL, without credentials in it")
	// ErrHostKeyChanged: the SSH server's key isn't the one pinned.
	ErrHostKeyChanged = errors.New("the remote's SSH host key changed")
)

// Credentials authenticate to a remote.
type Credentials struct {
	Username, Password string // HTTPS
	SSHPrivateKey      []byte // PEM (OpenSSH format)
	// SSHHostKey is the pinned host key (authorized_keys format); empty:
	// trust the first one (OnHostKey records it).
	SSHHostKey string
	OnHostKey  func(key string)
}

// Dialer connects to remotes through a netguard.Guard.
type Dialer struct {
	Guard   *netguard.Guard
	Timeout time.Duration // per connection attempt (default 30s)
}

func (d Dialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 30 * time.Second
}

// HTTPClient is the guarded client for http(s) remotes and LFS servers.
func (d Dialer) HTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil, // a proxy would hide the real target from the guard
			DialContext:           d.Guard.DialContext(d.timeout()),
			TLSHandshakeTimeout:   d.timeout(),
			ResponseHeaderTimeout: 2 * d.timeout(),
			IdleConnTimeout:       30 * time.Second,
		},
		// Redirects are followed (some hosts redirect to a canonical
		// URL); every connection still goes through the guard.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}

// ValidateURL checks a remote's form: http(s) or ssh, a host, a path, no
// credentials (they go in Credentials).
func ValidateURL(raw string) error {
	ep, err := transport.NewEndpoint(strings.TrimSpace(raw))
	if err != nil || ep.Host == "" || strings.Trim(ep.Path, "/") == "" {
		return ErrURL
	}
	switch ep.Protocol {
	case "https", "http":
		if ep.User != "" || ep.Password != "" {
			return ErrURL
		}
	case "ssh":
		if ep.Password != "" {
			return ErrURL
		}
	default:
		return ErrURL
	}
	return nil
}

// IsSSH reports whether a (valid) URL is an SSH remote.
func IsSSH(raw string) bool {
	ep, err := transport.NewEndpoint(strings.TrimSpace(raw))
	return err == nil && ep.Protocol == "ssh"
}

// Remote prepares a fetch from raw.
func (d Dialer) Remote(ctx context.Context, raw string, cred Credentials) (Remote, error) {
	if err := ValidateURL(raw); err != nil {
		return Remote{}, err
	}
	ep, _ := transport.NewEndpoint(strings.TrimSpace(raw))
	if ep.Protocol != "ssh" {
		client := d.HTTPClient()
		r := Remote{Endpoint: ep, Transport: githttp.NewClient(client), HTTPClient: client}
		if cred.Username != "" || cred.Password != "" {
			user := cred.Username
			if user == "" {
				user = "git" // tokens: hosts take any user name
			}
			r.Auth = &githttp.BasicAuth{Username: user, Password: cred.Password}
		}
		return r, nil
	}
	auth, err := d.sshAuth(ctx, ep, cred)
	if err != nil {
		return Remote{}, err
	}
	return Remote{Endpoint: ep, Auth: auth, Transport: gitssh.DefaultClient}, nil
}

// sshAuth resolves the host through the guard (go-git dials the checked
// IP, so the name can't point elsewhere later) and prepares the key and
// host key check.
func (d Dialer) sshAuth(ctx context.Context, ep *transport.Endpoint, cred Credentials) (*gitssh.PublicKeys, error) {
	if len(cred.SSHPrivateKey) == 0 {
		return nil, errors.New("an SSH remote needs the mirror's SSH key")
	}
	signer, err := ssh.ParsePrivateKey(cred.SSHPrivateKey)
	if err != nil {
		return nil, err
	}
	ip, err := d.Guard.ResolveIP(ctx, ep.Host)
	if err != nil {
		return nil, err
	}
	ep.Host = ip.String()
	user := ep.User
	if user == "" {
		user = "git"
	}
	return &gitssh.PublicKeys{User: user, Signer: signer, HostKeyCallbackHelper: gitssh.HostKeyCallbackHelper{
		HostKeyCallback: hostKeyCheck(cred),
	}}, nil
}

func hostKeyCheck(cred Credentials) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		got := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		if cred.SSHHostKey == "" {
			if cred.OnHostKey != nil {
				cred.OnHostKey(got) // trust on first use
			}
			return nil
		}
		if got != cred.SSHHostKey {
			return ErrHostKeyChanged
		}
		return nil
	}
}

// GenerateSSHKey makes a mirror's key pair: the private key (PEM) and the
// public key to add on the remote (authorized_keys format).
func GenerateSSHKey(comment string) ([]byte, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment, nil
}

// SafeURL is a remote URL for display (it holds no credentials; ValidateURL
// refuses them).
func SafeURL(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// describe turns transport errors into something a user can act on.
func Describe(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed):
		return "authentication failed: check the credentials (or, over SSH, that the mirror's key was added on the remote)"
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return "repository not found on the remote"
	case errors.Is(err, netguard.ErrBlockedAddress):
		return "the remote's address is private or local (allow it with mirrors.allowed_hosts)"
	}
	return fmt.Sprint(err)
}
