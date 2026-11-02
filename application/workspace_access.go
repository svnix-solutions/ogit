package application

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labbs/ogit/domain"
)

// Browser access rules:
//   - a workspace is only served on its own hosts, <id>.<workspaces.domain>
//     for the editor and <port>-<id>.<workspaces.domain> for what it listens
//     on, each its own origin, never the session's; these hosts serve nothing
//     else;
//   - only the workspace's user gets in, whatever anyone else's role, and
//     only with their own session on the main site: EnterURL turns that into
//     a one-time link for one host (a minute), Redeem into a cookie for that
//     host. The cookie is signed for the host and names the session, so it
//     can't be moved to another workspace or port;
//   - Route checks the session, the user and the workspace at least every
//     workspaceRecheck, for open connections too, so logging out, being
//     disabled or the workspace stopping ends access within that time;
//   - the server dials the agent at the address it reports, pins the
//     certificate it reports (agents make their own) and signs each request
//     with the agent's proxy key, handed out in syncs and never stored;
//   - requests from another origin that could change anything are refused
//     (TestWorkspaceBrowserOrigins): workspaces share a parent domain, so
//     their cookies don't stop one workspace's page calling another.

var (
	ErrWorkspaceNotRunning  = errors.New("this workspace isn't running")
	ErrWorkspaceAccess      = errors.New("sign in to open this workspace")
	ErrWorkspaceNoPublicURL = errors.New("browser access needs http.public_url")
)

const (
	workspaceTicketLifetime = time.Minute
	workspaceRecheck        = 30 * time.Second
	workspaceRouteCache     = 10000
)

// WorkspaceHost is a workspace's host, split. Port 0 is the editor.
type WorkspaceHost struct {
	Id   string
	Port int
}

func (h WorkspaceHost) label() string {
	if h.Port == 0 {
		return h.Id
	}
	return strconv.Itoa(h.Port) + "-" + h.Id
}

// ParseWorkspaceHost reads a host under the workspaces domain. Under says
// whether it is one, ok whether it names a workspace.
func ParseWorkspaceHost(host, workspacesDomain string) (h WorkspaceHost, under, ok bool) {
	host = strings.ToLower(host)
	base := strings.ToLower(workspacesDomain)
	if base == "" || (host != base && !strings.HasSuffix(host, "."+base)) {
		return h, false, false
	}
	label, found := strings.CutSuffix(host, "."+base)
	if !found || strings.Contains(label, ".") {
		return h, true, false
	}
	if workspaceId.MatchString(label) {
		return WorkspaceHost{Id: label}, true, true
	}
	port, id, found := strings.Cut(label, "-")
	n, err := strconv.Atoi(port)
	if !found || err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || !workspaceId.MatchString(id) {
		return h, true, false
	}
	return WorkspaceHost{Id: id, Port: n}, true, true
}

// origin is the host's base URL, in the scheme and port of the public URL.
func (c *WorkspaceApp) origin(h WorkspaceHost) (string, error) {
	public, err := url.Parse(c.Config.Server.PublicURL)
	if err != nil || public.Host == "" {
		return "", ErrWorkspaceNoPublicURL
	}
	host := h.label() + "." + c.Config.Workspaces.Domain
	if port := public.Port(); port != "" {
		host += ":" + port
	}
	return public.Scheme + "://" + host, nil
}

// SafeWorkspacePath keeps only a path on the same host.
func SafeWorkspacePath(path string) string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, "/\\") {
		return "/"
	}
	return path
}

// EnterURL makes the one-time link that signs this session's browser in to a
// host of the user's running workspace.
func (c *WorkspaceApp) EnterURL(userId, sessionId string, h WorkspaceHost, next string) (string, error) {
	if !workspaceId.MatchString(h.Id) {
		return "", ErrWorkspaceNotFound
	}
	w, err := c.Workspaces.Get(userId, h.Id)
	if isNotFound(err) {
		return "", ErrWorkspaceNotFound
	} else if err != nil {
		return "", err
	}
	if w.State != domain.WorkspaceRunning {
		return "", ErrWorkspaceNotRunning
	}
	origin, err := c.origin(h)
	if err != nil {
		return "", err
	}
	raw := randomToken("ogitt_")
	ticket := &domain.WorkspaceTicket{TokenHash: HashToken(raw), WorkspaceId: h.Id, SessionId: sessionId, Port: h.Port,
		ExpiresAt: time.Now().UTC().Add(workspaceTicketLifetime)}
	if err := c.Workspaces.CreateTicket(ticket); err != nil {
		return "", err
	}
	query := url.Values{"ticket": {raw}, "next": {SafeWorkspacePath(next)}}
	return origin + WorkspaceEnterPath + "?" + query.Encode(), nil
}

// WorkspaceEnterPath is the one path of a workspace's hosts that isn't theirs.
const WorkspaceEnterPath = "/__ogit/enter"

// WorkspaceLogin is the main site's route that starts signing in, for a host
// that got a request without a cookie.
func (c *WorkspaceApp) WorkspaceLogin(h WorkspaceHost, next string) (string, error) {
	public := strings.TrimSuffix(c.Config.Server.PublicURL, "/")
	if public == "" {
		return "", ErrWorkspaceNoPublicURL
	}
	query := url.Values{"next": {SafeWorkspacePath(next)}}
	if h.Port != 0 {
		query.Set("port", strconv.Itoa(h.Port))
	}
	return public + "/api/v1/workspaces/" + h.Id + "/enter?" + query.Encode(), nil
}

func (c *WorkspaceApp) cookieMAC(sessionId string, h WorkspaceHost) string {
	key := sha256.Sum256([]byte("ogit workspace cookie\x00" + c.Config.Session.SecretKey))
	mac := hmac.New(sha256.New, key[:])
	fmt.Fprintf(mac, "%s\x00%s", sessionId, h.label())
	return hex.EncodeToString(mac.Sum(nil))
}

// Redeem takes a one-time link for this host and returns the cookie's value.
func (c *WorkspaceApp) Redeem(ticket string, h WorkspaceHost) (string, error) {
	t, err := c.Workspaces.TakeTicket(HashToken(ticket), time.Now().UTC())
	if err != nil || t.WorkspaceId != h.Id || t.Port != h.Port {
		return "", ErrWorkspaceAccess
	}
	return t.SessionId + "." + c.cookieMAC(t.SessionId, h), nil
}

// WorkspaceRoute is where a host's requests go.
type WorkspaceRoute struct {
	AgentId     string
	URL         string
	Fingerprint string
	Key         string
}

type cachedRoute struct {
	route *WorkspaceRoute
	err   error
	until time.Time
}

// routes keeps Route's answers for workspaceRecheck, so that a page's many
// requests don't each cost several queries.
type routes struct {
	mu      sync.Mutex
	entries map[string]cachedRoute
}

func (r *routes) get(key string, now time.Time) (cachedRoute, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[key]
	if !ok || now.After(e.until) {
		return cachedRoute{}, false
	}
	return e, true
}

func (r *routes) put(key string, e cachedRoute) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil || len(r.entries) >= workspaceRouteCache {
		r.entries = map[string]cachedRoute{}
	}
	r.entries[key] = e
}

// Route checks a host's cookie and says where its requests go. Answers can
// be up to RouteCheck old; a failing check isn't kept.
func (c *WorkspaceApp) Route(cookie string, h WorkspaceHost) (*WorkspaceRoute, error) {
	now := time.Now()
	key := h.label() + "\x00" + cookie
	if e, ok := c.routes.get(key, now); ok {
		return e.route, e.err
	}
	route, err := c.route(cookie, h)
	if err == nil || errors.Is(err, ErrWorkspaceAccess) || errors.Is(err, ErrWorkspaceNotRunning) {
		c.routes.put(key, cachedRoute{route: route, err: err, until: now.Add(c.RouteCheck())})
	}
	return route, err
}

// RouteCheck is how old Route's answers may be.
func (c *WorkspaceApp) RouteCheck() time.Duration {
	if c.Recheck > 0 {
		return c.Recheck
	}
	return workspaceRecheck
}

func (c *WorkspaceApp) route(cookie string, h WorkspaceHost) (*WorkspaceRoute, error) {
	sessionId, mac, found := strings.Cut(cookie, ".")
	if !found || !hmac.Equal([]byte(mac), []byte(c.cookieMAC(sessionId, h))) {
		return nil, ErrWorkspaceAccess
	}
	session, err := c.Sessions.GetById(sessionId)
	if isNotFound(err) {
		return nil, ErrWorkspaceAccess
	} else if err != nil {
		return nil, err
	}
	if session.ExpiresAt.Before(time.Now()) {
		return nil, ErrWorkspaceAccess
	}
	user, err := c.Users.GetById(session.UserId)
	if isNotFound(err) {
		return nil, ErrWorkspaceAccess
	} else if err != nil {
		return nil, err
	}
	if !user.Active {
		return nil, ErrWorkspaceAccess
	}
	w, err := c.Workspaces.Get(session.UserId, h.Id)
	if isNotFound(err) {
		return nil, ErrWorkspaceAccess
	} else if err != nil {
		return nil, err
	}
	if w.State != domain.WorkspaceRunning || w.AgentId == nil {
		return nil, ErrWorkspaceNotRunning
	}
	agent, err := c.Workspaces.GetAgent(*w.AgentId)
	if isNotFound(err) {
		return nil, ErrWorkspaceNotRunning
	} else if err != nil {
		return nil, err
	}
	if agent.ProxyURL == "" || agent.ProxyFingerprint == "" {
		return nil, ErrWorkspaceNotRunning
	}
	return &WorkspaceRoute{AgentId: agent.Id, URL: agent.ProxyURL, Fingerprint: agent.ProxyFingerprint, Key: c.proxyKey(agent.Id)}, nil
}

// proxyKey signs the server's requests to an agent. It's derived, so servers
// sharing session.secret_key agree without storing it.
func (c *WorkspaceApp) proxyKey(agentId string) string {
	key := sha256.Sum256([]byte("ogit workspace agent proxy\x00" + c.Config.Session.SecretKey))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(agentId))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignProxyRequest is the signature of a request for a workspace's port, for
// the given time, in the X-Ogit-Proxy-Auth header.
func SignProxyRequest(key, id string, port int, at time.Time) string {
	unix := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(key))
	fmt.Fprintf(mac, "%s\n%s\n%d", unix, id, port)
	return unix + "." + hex.EncodeToString(mac.Sum(nil))
}

// VerifyProxyRequest checks a signature, within maxAge of now either way.
func VerifyProxyRequest(key, signature, id string, port int, now time.Time, maxAge time.Duration) bool {
	unix, _, found := strings.Cut(signature, ".")
	seconds, err := strconv.ParseInt(unix, 10, 64)
	if !found || err != nil {
		return false
	}
	at := time.Unix(seconds, 0)
	if at.Before(now.Add(-maxAge)) || at.After(now.Add(maxAge)) {
		return false
	}
	return hmac.Equal([]byte(signature), []byte(SignProxyRequest(key, id, port, at)))
}
