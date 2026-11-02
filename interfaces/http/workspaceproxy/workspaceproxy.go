// Package workspaceproxy serves workspaces' hosts, <id>.<workspaces.domain>
// and <port>-<id>.<workspaces.domain>, passing requests and WebSockets to the
// workspace's agent. The rules are in application/workspace_access.go.
package workspaceproxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/infrastructure/config"
	"github.com/labbs/ogit/infrastructure/wsagent"
	"github.com/labbs/ogit/interfaces/http/websession"
	"github.com/rs/zerolog"
)

const (
	cookieName       = "ogit_workspace"
	secureCookieName = "__Host-" + cookieName
	cookieLifetime   = 30 * 24 * time.Hour // the session decides, not the cookie
	bodyGrace        = 30 * time.Second
)

// hopHeaders aren't passed on either way. Forwarding headers from the
// browser are the server's to set, and X-Ogit-* the agent protocol's.
var hopHeaders = map[string]bool{"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true}

type Controller struct {
	Config     config.Config
	Logger     zerolog.Logger
	Workspaces *application.WorkspaceApp
	Sessions   *application.SessionApp

	transports transports
}

type transports struct {
	mu     sync.Mutex
	agents map[string]agentTransport
}

type agentTransport struct {
	fingerprint string
	transport   *http.Transport
}

// pinned accepts the certificate with that fingerprint, and no other.
func pinned(fingerprint string) *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Transport{DialContext: dialer.DialContext, DisableCompression: true, MaxIdleConnsPerHost: 32, IdleConnTimeout: time.Minute,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"},
			InsecureSkipVerify: true, // the pin below is the check
			VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("no certificate")
				}
				sum := sha256.Sum256(state.PeerCertificates[0].Raw)
				if hex.EncodeToString(sum[:]) != fingerprint {
					return errors.New("the agent's certificate isn't the one it reported")
				}
				return nil
			},
		}}
}

func (t *transports) get(route *application.WorkspaceRoute) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.agents == nil {
		t.agents = map[string]agentTransport{}
	}
	current, ok := t.agents[route.AgentId]
	if ok && current.fingerprint == route.Fingerprint {
		return current.transport
	}
	if ok {
		current.transport.CloseIdleConnections()
	}
	transport := pinned(route.Fingerprint)
	t.agents[route.AgentId] = agentTransport{fingerprint: route.Fingerprint, transport: transport}
	return transport
}

func (c *Controller) secure() bool { return strings.HasPrefix(c.Config.Server.PublicURL, "https://") }

func (c *Controller) cookie() string {
	if c.secure() {
		return secureCookieName
	}
	return cookieName
}

// Setup adds the main site's route that starts signing in, behind the
// session middleware.
func (c *Controller) Setup(app *fiber.App) {
	app.Get("/api/v1/workspaces/:workspace_id/enter", c.Enter)
}

// Enter sends a signed-in browser to a one-time link for the host.
func (c *Controller) Enter(ctx *fiber.Ctx) error {
	next := ctx.Query("next", "/")
	token, found := strings.CutPrefix(ctx.Get(fiber.HeaderAuthorization), "Bearer ")
	auth, err := c.Sessions.ValidateToken(token)
	if !found || err != nil {
		return ctx.Redirect("/login?next=" + url.QueryEscape(ctx.OriginalURL()))
	}
	sessionId, _ := auth.Claims[application.SessionIDClaim].(string)
	host := application.WorkspaceHost{Id: ctx.Params("workspace_id"), Port: ctx.QueryInt("port")}
	if host.Port < 0 || host.Port > 65535 {
		return ctx.Status(fiber.StatusNotFound).SendString(application.ErrWorkspaceNotFound.Error())
	}
	link, err := c.Workspaces.EnterURL(auth.UserID, sessionId, host, next)
	switch {
	case err == nil:
		ctx.Set(fiber.HeaderReferrerPolicy, "no-referrer")
		ctx.Set(fiber.HeaderCacheControl, "no-store")
		return ctx.Redirect(link)
	case errors.Is(err, application.ErrWorkspaceNotFound):
		return ctx.Status(fiber.StatusNotFound).SendString(err.Error())
	case errors.Is(err, application.ErrWorkspaceNotRunning):
		return ctx.Status(fiber.StatusConflict).SendString(err.Error())
	case errors.Is(err, application.ErrWorkspaceNoPublicURL):
		return ctx.Status(fiber.StatusServiceUnavailable).SendString(err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.workspaceproxy").Msg("signing in to a workspace failed")
	return ctx.Status(fiber.StatusInternalServerError).SendString("internal error")
}

// Middleware takes every request for the workspaces domain. Nothing under it
// reaches the site's routes, and other hosts never get here.
func (c *Controller) Middleware() fiber.Handler {
	base := c.Config.Workspaces.Domain
	return func(ctx *fiber.Ctx) error {
		host, under, ok := application.ParseWorkspaceHost(hostOnly(string(ctx.Context().Host())), base)
		if !under {
			return ctx.Next()
		}
		if !ok {
			return ctx.Status(fiber.StatusNotFound).SendString("not found")
		}
		return c.serve(ctx, host)
	}
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func wantsPage(ctx *fiber.Ctx) bool {
	if dest := ctx.Get("Sec-Fetch-Dest"); dest != "" {
		return dest == "document"
	}
	return ctx.Method() == fiber.MethodGet && strings.Contains(ctx.Get(fiber.HeaderAccept), "text/html")
}

// foreign reports whether a request that could change something, or open a
// socket, comes from a page of another origin.
func foreign(ctx *fiber.Ctx, upgrade bool) bool {
	origin := ctx.Get(fiber.HeaderOrigin)
	safe := ctx.Method() == fiber.MethodGet || ctx.Method() == fiber.MethodHead || ctx.Method() == fiber.MethodOptions
	if origin == "" || (safe && !upgrade) {
		return false
	}
	u, err := url.Parse(origin)
	return err != nil || !strings.EqualFold(u.Host, string(ctx.Context().Host()))
}

func (c *Controller) serve(ctx *fiber.Ctx, host application.WorkspaceHost) error {
	ctx.Set(fiber.HeaderReferrerPolicy, "no-referrer")
	if ctx.Path() == application.WorkspaceEnterPath {
		return c.redeem(ctx, host)
	}
	upgrade := ctx.Get(fiber.HeaderUpgrade) != ""
	if foreign(ctx, upgrade) {
		return ctx.Status(fiber.StatusForbidden).SendString("cross-origin request refused")
	}
	cookie := ctx.Cookies(c.cookie())
	route, err := c.Workspaces.Route(cookie, host)
	switch {
	case err == nil:
		return c.forward(ctx, host, cookie, route, upgrade)
	case errors.Is(err, application.ErrWorkspaceAccess):
		if wantsPage(ctx) {
			link, err := c.Workspaces.WorkspaceLogin(host, ctx.OriginalURL())
			if err == nil {
				return ctx.Redirect(link)
			}
		}
		return ctx.Status(fiber.StatusUnauthorized).SendString(application.ErrWorkspaceAccess.Error())
	case errors.Is(err, application.ErrWorkspaceNotRunning):
		return ctx.Status(fiber.StatusServiceUnavailable).SendString(err.Error())
	}
	c.Logger.Error().Err(err).Str("component", "http.workspaceproxy").Msg("routing to a workspace failed")
	return ctx.Status(fiber.StatusInternalServerError).SendString("internal error")
}

func (c *Controller) redeem(ctx *fiber.Ctx, host application.WorkspaceHost) error {
	value, err := c.Workspaces.Redeem(ctx.Query("ticket"), host)
	if err != nil {
		return ctx.Status(fiber.StatusUnauthorized).SendString("this link was used or has expired; open the workspace again")
	}
	ctx.Cookie(&fiber.Cookie{Name: c.cookie(), Value: value, Path: "/", HTTPOnly: true, Secure: c.secure() || websession.Secure(ctx, c.Config.Server.PublicURL),
		SameSite: fiber.CookieSameSiteLaxMode, Expires: time.Now().Add(cookieLifetime)})
	ctx.Set(fiber.HeaderCacheControl, "no-store")
	return ctx.Redirect(application.SafeWorkspacePath(ctx.Query("next", "/")))
}

// withoutCookie removes ours: what runs in a workspace never sees it.
func (c *Controller) withoutCookie(header string) string {
	kept := make([]string, 0, 4)
	for _, part := range strings.Split(header, ";") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if name != "" && name != cookieName && name != secureCookieName {
			kept = append(kept, strings.TrimSpace(part))
		}
	}
	return strings.Join(kept, "; ")
}

func (c *Controller) request(ctx *fiber.Ctx, reqCtx context.Context, host application.WorkspaceHost, route *application.WorkspaceRoute, body io.Reader, length int64) (*http.Request, error) {
	target := strings.TrimSuffix(route.URL, "/") + string(ctx.Request().RequestURI())
	req, err := http.NewRequestWithContext(reqCtx, ctx.Method(), target, body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	req.Host = string(ctx.Context().Host())
	ctx.Request().Header.VisitAll(func(key, value []byte) {
		name := http.CanonicalHeaderKey(string(key))
		if hopHeaders[name] || name == "Host" || name == "Content-Length" || name == "Forwarded" || name == "X-Real-Ip" ||
			strings.HasPrefix(name, "X-Forwarded-") || strings.HasPrefix(name, "X-Ogit-") {
			return
		}
		if name == "Cookie" {
			if value := c.withoutCookie(string(value)); value != "" {
				req.Header.Set(name, value)
			}
			return
		}
		req.Header.Add(name, string(value))
	})
	if upgrade := ctx.Get(fiber.HeaderUpgrade); upgrade != "" {
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", upgrade)
	}
	proto := "http"
	if c.secure() || ctx.Protocol() == "https" {
		proto = "https"
	}
	req.Header.Set("X-Forwarded-Proto", proto)
	req.Header.Set("X-Forwarded-Host", req.Host)
	req.Header.Set(wsagent.HeaderWorkspace, host.Id)
	req.Header.Set(wsagent.HeaderPort, strconv.Itoa(host.Port))
	req.Header.Set(wsagent.HeaderProxyAuth, application.SignProxyRequest(route.Key, host.Id, host.Port, time.Now()))
	return req, nil
}

// watch ends a connection once its route stops being valid.
func (c *Controller) watch(reqCtx context.Context, cancel func(), cookie string, host application.WorkspaceHost) {
	ticker := time.NewTicker(c.Workspaces.RouteCheck())
	defer ticker.Stop()
	for {
		select {
		case <-reqCtx.Done():
			return
		case <-ticker.C:
			_, err := c.Workspaces.Route(cookie, host)
			if errors.Is(err, application.ErrWorkspaceAccess) || errors.Is(err, application.ErrWorkspaceNotRunning) {
				cancel()
				return
			}
		}
	}
}

// bodyStream tells when the transport is done with the request's body, which
// fasthttp takes back when the handler returns.
type bodyStream struct {
	io.Reader
	once sync.Once
	done chan struct{}
}

func (b *bodyStream) Close() error {
	b.once.Do(func() { close(b.done) })
	return nil
}

// responseBody ends the connection's watch with the response.
type responseBody struct {
	io.ReadCloser
	cancel func()
}

func (r responseBody) Close() error {
	r.cancel()
	return r.ReadCloser.Close()
}

func (c *Controller) forward(ctx *fiber.Ctx, host application.WorkspaceHost, cookie string, route *application.WorkspaceRoute, upgrade bool) error {
	reqCtx, cancel := context.WithCancel(context.Background())
	var body io.Reader
	var stream *bodyStream
	length := int64(0)
	if ctx.Request().IsBodyStream() {
		stream = &bodyStream{Reader: ctx.Request().BodyStream(), done: make(chan struct{})}
		body, length = stream, int64(ctx.Request().Header.ContentLength())
		if length < 0 {
			length = -1
		}
		ctx.Context().SetConnectionClose()
	} else if len(ctx.Body()) > 0 {
		data := bytes.Clone(ctx.Body())
		body, length = bytes.NewReader(data), int64(len(data))
	}
	req, err := c.request(ctx, reqCtx, host, route, body, length)
	if err != nil {
		cancel()
		return ctx.Status(fiber.StatusBadGateway).SendString("the workspace can't be reached")
	}
	resp, err := c.transports.get(route).RoundTrip(req)
	if stream != nil {
		select {
		case <-stream.done:
		case <-time.After(bodyGrace):
			cancel()
			<-stream.done
		}
	}
	if err != nil {
		cancel()
		c.Logger.Warn().Err(err).Str("component", "http.workspaceproxy").Str("workspace_id", host.Id).Msg("reaching a workspace's agent failed")
		return ctx.Status(fiber.StatusBadGateway).SendString("the workspace can't be reached")
	}
	go c.watch(reqCtx, cancel, cookie, host)
	for name, values := range resp.Header {
		if hopHeaders[http.CanonicalHeaderKey(name)] || name == "Content-Length" {
			continue
		}
		for _, value := range values {
			ctx.Response().Header.Add(name, value)
		}
	}
	ctx.Status(resp.StatusCode)
	if resp.StatusCode == fiber.StatusSwitchingProtocols {
		return tunnel(ctx, resp, reqCtx, cancel)
	}
	size := -1
	if resp.ContentLength >= 0 {
		size = int(resp.ContentLength)
	}
	ctx.Response().SetBodyStream(responseBody{ReadCloser: resp.Body, cancel: cancel}, size)
	return nil
}

// tunnel joins the browser's connection and the agent's after an upgrade.
func tunnel(ctx *fiber.Ctx, resp *http.Response, reqCtx context.Context, cancel func()) error {
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		cancel()
		_ = resp.Body.Close()
		return ctx.Status(fiber.StatusBadGateway).SendString("the workspace can't be reached")
	}
	ctx.Set(fiber.HeaderUpgrade, resp.Header.Get(fiber.HeaderUpgrade))
	ctx.Set(fiber.HeaderConnection, "Upgrade")
	ctx.Context().Hijack(func(conn net.Conn) {
		// fasthttp takes conn back when this returns, so both copies must end
		// first. Its Close does nothing until then: a deadline wakes the read.
		end := func() {
			cancel()
			_ = conn.SetDeadline(time.Now())
			_ = upstream.Close()
		}
		defer end()
		go func() {
			<-reqCtx.Done()
			end()
		}()
		done := make(chan struct{}, 2)
		pipe := func(to io.Writer, from io.Reader) {
			_, _ = io.Copy(to, from)
			end()
			done <- struct{}{}
		}
		go pipe(upstream, conn)
		go pipe(conn, upstream)
		<-done
		<-done
	})
	return nil
}
