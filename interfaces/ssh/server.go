// Package ssh serves git over SSH. Everyone connects as "git"; the offered
// public key identifies a user key or a deploy key, and access follows the
// same rules as git over HTTP (application.AuthApp.AuthorizeSSH).
package ssh

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/labbs/ogit/infrastructure/ratelimit"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	helpergit "github.com/labbs/ogit/infrastructure/helpers/git"
	"github.com/labbs/ogit/infrastructure/storage"
	"github.com/rs/zerolog"
	gossh "golang.org/x/crypto/ssh"
)

const handshakeTimeout = 15 * time.Second

// pathPattern matches <namespace>/<slug>: a username or group path (see
// domain.ValidNamespaceName and ValidGroupPath) and a gosimple/slug slug. Anything else in a
// command, including path traversal, is rejected.
var pathPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._-]*){0,4}/[a-z0-9][a-z0-9-]*(\.wiki)?$`) // up to 5 group levels; .wiki: its wiki

// flatPattern is a repository path from before namespaces (one segment).
var flatPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Server struct {
	Logger  zerolog.Logger
	AuthApp *application.AuthApp
	Keys    domain.SSHKeyPers
	Storage storage.Config
	// Limits counts git operations per key (ratelimit.git_user); nil: none.
	Limits *ratelimit.Guard
	// Webhooks receives push events; nil: none.
	Webhooks *application.WebhookApp
	// Protection checks pushes to protected branches; nil: none.
	Protection *application.ProtectionApp
	// Pulls updates pull requests from forks when their branch is pushed.
	Pulls *application.PullRequestApp
	// LFS checks file locks on pushes and answers git-lfs-authenticate;
	// nil: none.
	LFS *application.LFSApp
	// MaxPushSize bounds a push's pack in bytes (git.max_push_size); 0: none.
	MaxPushSize int64

	config *gossh.ServerConfig
}

// New loads (or creates) the host key and prepares the server.
func New(hostKeyPath string, logger zerolog.Logger, auth *application.AuthApp, keys domain.SSHKeyPers, store storage.Config) (*Server, error) {
	signer, err := loadOrCreateHostKey(hostKeyPath, logger)
	if err != nil {
		return nil, err
	}
	s := &Server{Logger: logger.With().Str("component", "ssh").Logger(), AuthApp: auth, Keys: keys, Storage: store}
	s.config = &gossh.ServerConfig{
		ServerVersion:     "SSH-2.0-oGit",
		PublicKeyCallback: s.authenticate,
	}
	s.config.AddHostKey(signer)
	return s, nil
}

// Serve accepts connections until ln is closed.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) authenticate(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
	if meta.User() != "git" {
		return nil, errors.New(`connect as "git"`)
	}
	fp := gossh.FingerprintSHA256(key)
	if _, err := s.AuthApp.IdentifySSHKey(s.Keys, fp); err != nil {
		return nil, errors.New("unknown key")
	}
	return &gossh.Permissions{Extensions: map[string]string{"fingerprint": fp}}, nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	sconn, chans, reqs, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		s.Logger.Debug().Err(err).Str("remote", conn.RemoteAddr().String()).Msg("handshake failed")
		return
	}
	defer sconn.Close()
	_ = conn.SetDeadline(time.Time{})
	go gossh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(gossh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(sconn, ch, chReqs)
	}
}

func (s *Server) handleSession(sconn *gossh.ServerConn, ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()

	// Identify again: the key may have been removed or its user disabled
	// since the handshake.
	id, err := s.AuthApp.IdentifySSHKey(s.Keys, sconn.Permissions.Extensions["fingerprint"])
	if err != nil {
		exit(ch, 1)
		return
	}

	for req := range reqs {
		switch req.Type {
		case "env", "pty-req":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			fmt.Fprintf(ch.Stderr(), "Hi %s! You've successfully authenticated, but oGit does not provide shell access.\n", id.Name)
			exit(ch, 1)
			return
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			exit(ch, s.runGit(ch, id, payload.Command))
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// runGit serves one git command and returns its exit status.
func (s *Server) runGit(ch gossh.Channel, id *application.SSHIdentity, command string) uint32 {
	logger := s.Logger.With().Str("key", id.Key.Fingerprint).Str("as", id.Name).Logger()

	service, slug, ok := parseCommand(command)
	lfsOp := ""
	if !ok {
		service, slug, lfsOp, ok = parseLFSAuthenticate(command)
	}
	if !ok {
		fmt.Fprintln(ch.Stderr(), "oGit only serves git-upload-pack and git-receive-pack on <owner>/<name>.git")
		return 1
	}
	if flatPattern.MatchString(slug) {
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q not found: repositories are addressed as <owner>/<name>.git\n", slug)
		return 1
	}

	repo, pusher, err := s.AuthApp.AuthorizeSSHAs(id, slug, service)
	switch {
	case err == nil:
	case errors.Is(err, application.ErrGitArchived):
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q is archived (read-only)\n", slug)
		return 1
	case errors.Is(err, application.ErrGitMirror):
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q is a mirror (read-only)\n", slug)
		return 1
	case errors.Is(err, application.ErrGitTwoFactorRequired):
		fmt.Fprintf(ch.Stderr(), "fatal: two-factor authentication is required on this server; set it up in your settings\n")
		return 1
	case errors.Is(err, application.ErrGitForbidden):
		fmt.Fprintf(ch.Stderr(), "fatal: permission to %s denied to %s\n", slug, id.Name)
		return 1
	case errors.Is(err, application.ErrGitNotFound):
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q not found\n", slug)
		return 1
	default:
		logger.Error().Err(err).Msg("ssh authorization failed")
		fmt.Fprintln(ch.Stderr(), "fatal: internal error")
		return 1
	}
	if s.Limits != nil {
		if r := s.Limits.Allow(context.Background(), s.Limits.Rules.GitUser, "git:key:"+id.Key.Id); !r.Allowed {
			fmt.Fprintf(ch.Stderr(), "fatal: too many git requests; try again in %s seconds\n", ratelimit.RetryAfter(r))
			return 1
		}
	}
	_ = s.Keys.TouchLastUsed(id.Key.Id, time.Now())
	if lfsOp != "" {
		return s.lfsAuthenticate(ch, repo, pusher, lfsOp == "upload")
	}

	srv, ep, err := helpergit.GetTransportServer(repo.StorageKey, s.Storage)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q not found\n", slug)
		return 1
	}

	objects, err := s.Storage.GetStorer(repo.StorageKey)
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q not found\n", slug)
		return 1
	}

	ctx := context.Background()
	in := bufio.NewReader(ch)
	switch service {
	case "git-upload-pack":
		err = serveUploadPack(ctx, srv, ep, objects, in, ch)
	case "git-receive-pack":
		var updates []application.RefUpdate
		updates, err = serveReceivePack(ctx, ep, objects, in, ch, application.LimitPush(pusher, application.Chain(s.Protection.PushCheck(repo, pusher, objects), s.LFS.PushCheck(repo, pusher, objects))), s.MaxPushSize)
		s.Webhooks.Pushed(repo, pusher, objects, updates)
		s.Pulls.SourcePushed(repo, pusher, updates)
	}
	if err != nil {
		logger.Warn().Err(err).Str("service", service).Str("repo", slug).Msg("git over ssh failed")
		return 1
	}
	return 0
}

// parseCommand accepts "git-upload-pack 'owner/repo.git'" (also "git
// upload-pack", "/owner/repo.git", "~/owner/repo.git" or without .git) and
// returns the service and the repository path. A flat path ("repo.git") is
// returned too, so the caller can explain the new form.
func parseCommand(command string) (service, path string, ok bool) {
	command = strings.TrimSpace(command)
	for _, svc := range []string{"git-upload-pack", "git-receive-pack"} {
		alt := strings.Replace(svc, "-", " ", 1)
		var arg string
		switch {
		case strings.HasPrefix(command, svc+" "):
			arg = command[len(svc)+1:]
		case strings.HasPrefix(command, alt+" "):
			arg = command[len(alt)+1:]
		default:
			continue
		}
		arg = strings.Trim(strings.TrimSpace(arg), `'"`)
		arg = strings.TrimPrefix(strings.TrimPrefix(arg, "~"), "/")
		arg = strings.TrimSuffix(strings.TrimSuffix(arg, "/"), ".git")
		if !pathPattern.MatchString(arg) && !flatPattern.MatchString(arg) {
			return "", "", false
		}
		return svc, arg, true
	}
	return "", "", false
}

// parseLFSAuthenticate accepts "git-lfs-authenticate <path> download|upload"
// and returns the git service it needs (read or write).
func parseLFSAuthenticate(command string) (service, path, op string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) != 3 || fields[0] != "git-lfs-authenticate" {
		return "", "", "", false
	}
	switch fields[2] {
	case "download":
		service = "git-upload-pack"
	case "upload":
		service = "git-receive-pack"
	default:
		return "", "", "", false
	}
	_, path, ok = parseCommand(service + " " + fields[1])
	return service, path, fields[2], ok
}

// lfsAuthenticate answers git-lfs-authenticate: where the LFS API is, with a
// short-lived token for it.
func (s *Server) lfsAuthenticate(ch gossh.Channel, repo *domain.Repository, pusher application.Pusher, write bool) uint32 {
	if s.LFS == nil || s.LFS.Config.Server.PublicURL == "" {
		fmt.Fprintln(ch.Stderr(), "fatal: Git LFS over SSH needs the server's public URL (http.public_url)")
		return 1
	}
	token, ttl := s.LFS.Token(s.LFS.AccessFor(repo, pusher, write))
	out, _ := json.Marshal(map[string]any{
		"href":       strings.TrimSuffix(s.LFS.Config.Server.PublicURL, "/") + "/" + repo.GitPath() + ".git/info/lfs",
		"header":     map[string]string{"Authorization": "Bearer " + token},
		"expires_in": int(ttl.Seconds()),
	})
	_, _ = ch.Write(append(out, '\n'))
	return 0
}

// exit sends the exit status and closes the channel. The status must reach
// the client before the channel closes, or git reports "the remote end hung
// up unexpectedly".
func exit(ch gossh.Channel, status uint32) {
	_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{status}))
	_ = ch.CloseWrite()
	_ = ch.Close()
}

// isFlush reports whether the client sent only a flush-pkt ("0000"): it has
// nothing to fetch or push.
func isFlush(in *bufio.Reader) (bool, error) {
	head, err := in.Peek(4)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return true, nil
		}
		return false, err
	}
	return string(head) == "0000", nil
}

func loadOrCreateHostKey(path string, logger zerolog.Logger) (gossh.Signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return gossh.ParsePrivateKey(data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read ssh host key: %w", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := gossh.MarshalPrivateKey(priv, "ogit host key")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("write ssh host key: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	logger.Warn().Str("path", path).Str("fingerprint", gossh.FingerprintSHA256(signer.PublicKey())).
		Msg("generated a new SSH host key; keep this file, clients warn when it changes")
	return signer, nil
}
