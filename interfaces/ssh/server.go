// Package ssh serves git over SSH. Everyone connects as "git"; the offered
// public key identifies a user key or a deploy key, and access follows the
// same rules as git over HTTP (application.AuthApp.AuthorizeSSH).
package ssh

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
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

// slugPattern matches repository slugs (gosimple/slug output); anything else
// in a command, including path traversal, is rejected.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Server struct {
	Logger  zerolog.Logger
	AuthApp *application.AuthApp
	Keys    domain.SSHKeyPers
	Storage storage.Config

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
	if !ok {
		fmt.Fprintln(ch.Stderr(), "oGit only serves git-upload-pack and git-receive-pack")
		return 1
	}

	repo, err := s.AuthApp.AuthorizeSSH(id, slug, service)
	switch {
	case err == nil:
	case errors.Is(err, application.ErrGitArchived):
		fmt.Fprintf(ch.Stderr(), "fatal: repository %q is archived (read-only)\n", slug)
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
	_ = s.Keys.TouchLastUsed(id.Key.Id, time.Now())

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
		err = serveReceivePack(ctx, srv, ep, in, ch)
	}
	if err != nil {
		logger.Warn().Err(err).Str("service", service).Str("repo", slug).Msg("git over ssh failed")
		return 1
	}
	return 0
}

// parseCommand accepts "git-upload-pack 'repo.git'" (also "git upload-pack",
// "/repo.git", "~/repo.git" or without .git) and returns the service and slug.
func parseCommand(command string) (service, slug string, ok bool) {
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
		if !slugPattern.MatchString(arg) {
			return "", "", false
		}
		return svc, arg, true
	}
	return "", "", false
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
