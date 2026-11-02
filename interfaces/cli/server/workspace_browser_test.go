package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labbs/ogit/application"
	"github.com/labbs/ogit/domain"
	"github.com/labbs/ogit/infrastructure/wsagent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workspaceBackend stands for what listens in a container: it tells what it
// received, and echoes on an upgraded connection.
func workspaceBackend(t *testing.T, name string) (*httptest.Server, int) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			fmt.Fprint(buf, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			buf.Flush()
			line, err := buf.ReadString('\n')
			for err == nil {
				fmt.Fprint(buf, "echo "+line)
				buf.Flush()
				line, err = buf.ReadString('\n')
			}
			return
		}
		body, _ := io.ReadAll(r.Body)
		http.SetCookie(w, &http.Cookie{Name: "app", Value: "1"})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "host": r.Host, "uri": r.RequestURI, "headers": r.Header, "size": len(body)})
	}))
	t.Cleanup(server.Close)
	return server, server.Listener.Addr().(*net.TCPAddr).Port
}

func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().String()
}

type browserFixture struct {
	*agentFixture
	id      string
	devPort int
	address string
}

func newBrowserFixture(t *testing.T) *browserFixture {
	f := newAgentFixture(t)
	f.srv.deps.WorkspaceApp.Recheck = 10 * time.Millisecond
	f.srv.deps.WorkspaceApp.Config.Workspaces.MaxRunning = 5
	_, port, err := net.SplitHostPort(strings.TrimPrefix(f.srv.url, "http://"))
	require.NoError(t, err)
	f.srv.deps.WorkspaceApp.Config.Server.PublicURL = "http://ogit.test:" + port
	_, editor := workspaceBackend(t, "editor")
	_, devPort := workspaceBackend(t, "dev")
	f.agent.runtime.editor = editor
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	address := freeAddress(t)
	require.NoError(t, f.agent.ServeProxy(ctx, address, "https://"+address))
	return &browserFixture{agentFixture: f, id: f.running(t, "browser"), devPort: devPort, address: address}
}

// wsBrowser is one browser: workspace hosts resolve to the test server.
type wsBrowser struct {
	f      *browserFixture
	client *http.Client
}

func (f *browserFixture) browser(t *testing.T) *wsBrowser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	address := strings.TrimPrefix(f.srv.url, "http://")
	dialer := &net.Dialer{}
	return &wsBrowser{f: f, client: &http.Client{Jar: jar, Timeout: asyncWait,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address)
		}}}}
}

func (b *wsBrowser) host(label string) string {
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(b.f.srv.url, "http://"))
	return label + ".workspaces.test:" + port
}

func (b *wsBrowser) do(t *testing.T, method, rawURL string, headers map[string]string, body io.Reader) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, body)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := b.client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(data)
}

// link gets a one-time link as the signed-in user.
func (b *wsBrowser) link(t *testing.T, as user, id string, port int) string {
	t.Helper()
	resp, body := b.do(t, http.MethodGet, b.f.srv.url+fmt.Sprintf("/api/v1/workspaces/%s/enter?port=%d&next=/start%%3Fx=1", id, port),
		map[string]string{"Authorization": "Bearer " + as.session}, nil)
	require.Equal(t, http.StatusFound, resp.StatusCode, body)
	return resp.Header.Get("Location")
}

func (b *wsBrowser) enter(t *testing.T, as user, id string, port int) {
	t.Helper()
	resp, body := b.do(t, http.MethodGet, b.link(t, as, id, port), nil, nil)
	require.Equal(t, http.StatusFound, resp.StatusCode, body)
	require.Equal(t, "/start?x=1", resp.Header.Get("Location"))
}

func label(id string, port int) string {
	if port == 0 {
		return id
	}
	return strconv.Itoa(port) + "-" + id
}

func (b *wsBrowser) url(id string, port int, path string) string {
	return "http://" + b.host(label(id, port)) + path
}

// dial opens a WebSocket-like connection; the status is the answer's.
func (b *wsBrowser) dial(t *testing.T, host, cookie, origin string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(b.f.srv.url, "http://"), asyncWait)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetDeadline(time.Now().Add(asyncWait)))
	fmt.Fprintf(conn, "GET /socket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nCookie: %s\r\nOrigin: %s\r\n\r\n", host, cookie, origin)
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	return conn, reader, resp.StatusCode
}

func (b *wsBrowser) cookie(t *testing.T, id string, port int) string {
	t.Helper()
	u, err := url.Parse(b.url(id, port, "/"))
	require.NoError(t, err)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == "ogit_workspace" {
			return c.String()
		}
	}
	t.Fatal("no cookie")
	return ""
}

func decode(t *testing.T, body string) (out struct {
	Name    string              `json:"name"`
	Host    string              `json:"host"`
	URI     string              `json:"uri"`
	Size    int                 `json:"size"`
	Headers map[string][]string `json:"headers"`
}) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(body), &out), body)
	return out
}

func TestWorkspaceBrowserAccess(t *testing.T) {
	f := newBrowserFixture(t)
	b := f.browser(t)

	t.Run("signing in", func(t *testing.T) {
		resp, _ := b.do(t, http.MethodGet, b.url(f.id, 0, "/page?a=1"), map[string]string{"Accept": "text/html"}, nil)
		require.Equal(t, http.StatusFound, resp.StatusCode, "a page goes to the main site")
		assert.Contains(t, resp.Header.Get("Location"), "http://ogit.test:")
		assert.Contains(t, resp.Header.Get("Location"), "/api/v1/workspaces/"+f.id+"/enter")
		assert.Contains(t, resp.Header.Get("Location"), "next=%2Fpage%3Fa%3D1")
		resp, _ = b.do(t, http.MethodPost, b.url(f.id, 0, "/page"), nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "anything else is refused")

		resp, _ = b.do(t, http.MethodGet, f.srv.url+"/api/v1/workspaces/"+f.id+"/enter", nil, nil)
		require.Equal(t, http.StatusFound, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Location"), "/login?next=", "anonymous visitors sign in first")

		b.enter(t, f.dev, f.id, 0)
		resp, body := b.do(t, http.MethodGet, b.url(f.id, 0, "/some/path?q=1"), nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Equal(t, "/some/path?q=1", decode(t, body).URI)
		assert.Equal(t, "editor", decode(t, body).Name)
	})

	t.Run("only the workspace's user", func(t *testing.T) {
		for _, other := range []user{f.owner, f.maint, f.admin} {
			resp, _ := b.do(t, http.MethodGet, f.srv.url+"/api/v1/workspaces/"+f.id+"/enter", map[string]string{"Authorization": "Bearer " + other.session}, nil)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode, other.name)
		}
	})

	t.Run("a session of someone else", func(t *testing.T) {
		// Enter refuses them; this is the check behind it, on every request.
		var session domain.Session
		require.NoError(t, f.srv.deps.Database.Db.Where("user_id <> ?", "").Not("user_id = (SELECT user_id FROM workspace WHERE id = ?)", f.id).First(&session).Error)
		const raw = "ogitt_handmade"
		require.NoError(t, f.srv.deps.WorkspaceApp.Workspaces.CreateTicket(&domain.WorkspaceTicket{TokenHash: application.HashToken(raw), WorkspaceId: f.id, SessionId: session.Id, ExpiresAt: time.Now().UTC().Add(time.Minute)}))
		other := f.browser(t)
		resp, _ := other.do(t, http.MethodGet, other.url(f.id, 0, application.WorkspaceEnterPath+"?ticket="+raw), nil, nil)
		require.Equal(t, http.StatusFound, resp.StatusCode)
		resp, _ = other.do(t, http.MethodPost, other.url(f.id, 0, "/"), nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("links work once, on their host", func(t *testing.T) {
		other := f.browser(t)
		link := other.link(t, f.dev, f.id, 0)
		resp, _ := other.do(t, http.MethodGet, strings.Replace(link, f.id+".", "3000-"+f.id+".", 1), nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "another host")
		link = other.link(t, f.dev, f.id, 0)
		resp, _ = other.do(t, http.MethodGet, link, nil, nil)
		require.Equal(t, http.StatusFound, resp.StatusCode)
		again := f.browser(t)
		resp, _ = again.do(t, http.MethodGet, link, nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "used")
		link = other.link(t, f.dev, f.id, 0)
		require.NoError(t, f.srv.deps.Database.Db.Exec("UPDATE workspace_ticket SET expires_at = ?", time.Now().UTC().Add(-time.Second)).Error)
		resp, _ = again.do(t, http.MethodGet, link, nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "expired")
	})

	t.Run("redirects stay on the host", func(t *testing.T) {
		for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example/"} {
			resp, _ := b.do(t, http.MethodGet, f.srv.url+"/api/v1/workspaces/"+f.id+"/enter?next="+url.QueryEscape(next), map[string]string{"Authorization": "Bearer " + f.dev.session}, nil)
			require.Equal(t, http.StatusFound, resp.StatusCode)
			u, err := url.Parse(resp.Header.Get("Location"))
			require.NoError(t, err)
			assert.Equal(t, "/", u.Query().Get("next"), next)
		}
	})

	t.Run("what the workspace receives", func(t *testing.T) {
		req := map[string]string{"X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "gopher", "X-Forwarded-For": "10.1.2.3", "X-Ogit-Proxy-Auth": "forged", "Cookie": "mine=1"}
		resp, body := b.do(t, http.MethodGet, b.url(f.id, 0, "/"), req, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		got := decode(t, body)
		assert.Equal(t, b.host(f.id), got.Host)
		assert.Equal(t, []string{b.host(f.id)}, got.Headers["X-Forwarded-Host"])
		assert.Equal(t, []string{"http"}, got.Headers["X-Forwarded-Proto"])
		assert.Empty(t, got.Headers["X-Forwarded-For"], "what a browser claims isn't passed on")
		assert.Contains(t, strings.Join(got.Headers["Cookie"], ";"), "mine=1")
		assert.NotContains(t, strings.Join(got.Headers["Cookie"], ";"), "ogit_workspace", "never our cookie")
		for name := range got.Headers {
			assert.False(t, strings.HasPrefix(name, "X-Ogit-"), name)
		}
		assert.Contains(t, resp.Header.Values("Set-Cookie"), "app=1", "its cookies reach the browser")
	})

	t.Run("cookies are for one host", func(t *testing.T) {
		cookie := b.cookie(t, f.id, 0)
		resp, _ := b.do(t, http.MethodGet, b.url(f.id, f.devPort, "/"), map[string]string{"Cookie": cookie}, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "another port")
		second := f.running(t, "second")
		resp, _ = b.do(t, http.MethodGet, b.url(second, 0, "/"), map[string]string{"Cookie": cookie}, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "another workspace")
		status, _ := f.srv.post(t, workspacePath(second, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		f.agent.sync(t)
	})

	t.Run("ports", func(t *testing.T) {
		b.enter(t, f.dev, f.id, f.devPort)
		resp, body := b.do(t, http.MethodGet, b.url(f.id, f.devPort, "/"), nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Equal(t, "dev", decode(t, body).Name)
	})

	t.Run("nothing of the site on these hosts", func(t *testing.T) {
		_, body := b.do(t, http.MethodGet, b.url(f.id, 0, "/api/health"), nil, nil)
		assert.Equal(t, "editor", decode(t, body).Name, "the workspace's, not ours")
		for _, host := range []string{"workspaces.test", "nonsense.workspaces.test", "0-" + f.id + ".workspaces.test", "70000-" + f.id + ".workspaces.test", "a.b.workspaces.test"} {
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(f.srv.url, "http://"))
			resp, _ := b.do(t, http.MethodGet, "http://"+host+":"+port+"/api/health", nil, nil)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode, host)
		}
		resp, _ := b.do(t, http.MethodGet, f.srv.url+"/api/health", nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, "other hosts are the site")
	})

	t.Run("bodies over the site's limit pass", func(t *testing.T) {
		const size = 8 << 20
		resp, body := b.do(t, http.MethodPost, b.url(f.id, 0, "/upload"), map[string]string{"Origin": "http://" + b.host(f.id)}, strings.NewReader(strings.Repeat("x", size)))
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		assert.Equal(t, size, decode(t, body).Size)
	})

	t.Run("sockets", func(t *testing.T) {
		conn, reader, status := b.dial(t, b.host(f.id), b.cookie(t, f.id, 0), "http://"+b.host(f.id))
		require.Equal(t, http.StatusSwitchingProtocols, status)
		fmt.Fprint(conn, "hello\n")
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		assert.Equal(t, "echo hello\n", line)
	})

	t.Run("a stopped workspace", func(t *testing.T) {
		other := f.running(t, "stopping")
		late := f.browser(t)
		late.enter(t, f.dev, other, 0)
		status, _ := f.srv.post(t, workspacePath(other, "stop"), f.dev.session, nil)
		require.Equal(t, http.StatusOK, status)
		assert.Eventually(t, func() bool {
			resp, _ := late.do(t, http.MethodGet, late.url(other, 0, "/"), nil, nil)
			return resp.StatusCode == http.StatusServiceUnavailable
		}, asyncWait, 10*time.Millisecond)
		f.agent.sync(t)
		resp, _ := late.do(t, http.MethodGet, f.srv.url+"/api/v1/workspaces/"+other+"/enter", map[string]string{"Authorization": "Bearer " + f.dev.session}, nil)
		assert.Equal(t, http.StatusConflict, resp.StatusCode)
	})
}

// A page on one workspace's host could otherwise call another's: they share a
// parent domain, so the browser sends SameSite cookies.
func TestWorkspaceBrowserOrigins(t *testing.T) {
	f := newBrowserFixture(t)
	b := f.browser(t)
	b.enter(t, f.dev, f.id, 0)
	sibling := "http://3000-" + f.id + ".workspaces.test"

	_, _, status := b.dial(t, b.host(f.id), b.cookie(t, f.id, 0), sibling)
	assert.Equal(t, http.StatusForbidden, status, "sockets")
	resp, _ := b.do(t, http.MethodPost, b.url(f.id, 0, "/x"), map[string]string{"Origin": sibling}, strings.NewReader("{}"))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "writes")
	resp, _ = b.do(t, http.MethodPost, b.url(f.id, 0, "/x"), map[string]string{"Origin": "http://" + b.host(f.id)}, strings.NewReader("{}"))
	assert.Equal(t, http.StatusOK, resp.StatusCode, "its own pages")
}

func TestWorkspaceBrowserEndsWithTheSession(t *testing.T) {
	f := newBrowserFixture(t)
	refused := func(b *wsBrowser) func() bool {
		return func() bool {
			resp, _ := b.do(t, http.MethodPost, b.url(f.id, 0, "/"), nil, nil)
			return resp.StatusCode == http.StatusUnauthorized
		}
	}
	closed := func(t *testing.T, conn net.Conn, reader *bufio.Reader) {
		t.Helper()
		require.NoError(t, conn.SetDeadline(time.Now().Add(asyncWait)))
		_, err := reader.ReadString('\n')
		assert.ErrorIs(t, err, io.EOF, "open connections end too")
	}

	t.Run("logging out", func(t *testing.T) {
		status, login := f.srv.post(t, "/api/v1/auth/login", "", map[string]any{"email": "dev@example.com", "password": "Correct-Horse-Battery-9"})
		require.Equal(t, http.StatusOK, status, "%v", login)
		session := login["token"].(string)
		b := f.browser(t)
		b.enter(t, user{name: "dev", session: session}, f.id, 0)
		resp, body := b.do(t, http.MethodGet, b.url(f.id, 0, "/"), nil, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode, body)
		conn, reader, status := b.dial(t, b.host(f.id), b.cookie(t, f.id, 0), "http://"+b.host(f.id))
		require.Equal(t, http.StatusSwitchingProtocols, status)
		status, _ = f.srv.post(t, "/api/v1/auth/logout", session, map[string]any{})
		require.Equal(t, http.StatusOK, status)
		assert.Eventually(t, refused(b), asyncWait, 10*time.Millisecond)
		closed(t, conn, reader)
	})

	t.Run("a user's state, whatever their sessions", func(t *testing.T) {
		b := f.browser(t)
		b.enter(t, f.dev, f.id, 0)
		active := func(value bool) {
			require.NoError(t, f.srv.deps.Database.Db.Model(&domain.User{}).Where("username = ?", "dev").Update("active", value).Error)
		}
		active(false)
		defer active(true)
		assert.Eventually(t, refused(b), asyncWait, 10*time.Millisecond)
	})

	t.Run("being disabled", func(t *testing.T) {
		b := f.browser(t)
		b.enter(t, f.dev, f.id, 0)
		status, body := f.srv.request(t, http.MethodPatch, "/api/v1/admin/users/dev", f.admin.session, map[string]any{"active": false})
		require.Equal(t, http.StatusOK, status, "%v", body)
		assert.Eventually(t, refused(b), asyncWait, 10*time.Millisecond)
	})
}

func TestWorkspaceAgentListener(t *testing.T) {
	f := newBrowserFixture(t)
	b := f.browser(t)
	b.enter(t, f.dev, f.id, 0)
	app := f.srv.deps.WorkspaceApp

	direct := httptest.NewUnstartedServer(f.agent.ProxyHandler())
	direct.StartTLS()
	t.Cleanup(direct.Close)
	call := func(headers map[string]string) int {
		req, err := http.NewRequest(http.MethodGet, direct.URL+"/", nil)
		require.NoError(t, err)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := direct.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	route, err := app.Route(strings.TrimPrefix(b.cookie(t, f.id, 0), "ogit_workspace="), application.WorkspaceHost{Id: f.id})
	require.NoError(t, err)
	signed := func(key, id string, port int, at time.Time) map[string]string {
		return map[string]string{wsagent.HeaderWorkspace: id, wsagent.HeaderPort: strconv.Itoa(port), wsagent.HeaderProxyAuth: application.SignProxyRequest(key, id, port, at)}
	}

	assert.Equal(t, http.StatusOK, call(signed(route.Key, f.id, 0, time.Now())), "the server")
	assert.Equal(t, http.StatusForbidden, call(nil), "unsigned")
	assert.Equal(t, http.StatusForbidden, call(signed("wrong", f.id, 0, time.Now())), "another key")
	assert.Equal(t, http.StatusForbidden, call(signed(route.Key, f.id, 0, time.Now().Add(-time.Hour))), "old")
	moved := signed(route.Key, f.id, 0, time.Now())
	moved[wsagent.HeaderPort] = strconv.Itoa(f.devPort)
	assert.Equal(t, http.StatusForbidden, call(moved), "signed for another port")
	assert.Equal(t, http.StatusBadGateway, call(signed(route.Key, "00000000-0000-4000-8000-000000000000", 0, time.Now())), "not running")

	t.Run("reports must be usable", func(t *testing.T) {
		agent, err := app.AuthenticateAgent(f.agent.Client.Token)
		require.NoError(t, err)
		good := strings.Repeat("a", 64)
		for name, report := range map[string]application.AgentReport{
			"http":        {ProxyURL: "http://agent:1", ProxyFingerprint: good},
			"credentials": {ProxyURL: "https://a:b@agent:1", ProxyFingerprint: good},
			"fingerprint": {ProxyURL: "https://agent:1", ProxyFingerprint: "abc"},
		} {
			require.NoError(t, app.Report(agent, report), name)
			got, err := app.Workspaces.GetAgent(agent.Id)
			require.NoError(t, err)
			assert.Empty(t, got.ProxyURL, name)
			assert.Empty(t, got.ProxyFingerprint, name)
		}
		f.agent.sync(t)
	})

	t.Run("the certificate is pinned", func(t *testing.T) {
		assert.Eventually(t, func() bool {
			resp, _ := b.do(t, http.MethodGet, b.url(f.id, 0, "/"), nil, nil)
			return resp.StatusCode == http.StatusOK
		}, asyncWait, 10*time.Millisecond)
		sum := sha256.Sum256([]byte("another certificate"))
		f.agent.ProxyFingerprint = hex.EncodeToString(sum[:])
		f.agent.sync(t)
		assert.Eventually(t, func() bool {
			resp, _ := b.do(t, http.MethodGet, b.url(f.id, 0, "/"), nil, nil)
			return resp.StatusCode == http.StatusBadGateway
		}, asyncWait, 10*time.Millisecond)
	})
}

func TestParseWorkspaceHost(t *testing.T) {
	const id = "0a1b2c3d-1111-4222-8333-444455556666"
	for host, want := range map[string]application.WorkspaceHost{
		id + ".w.test":                  {Id: id},
		"3000-" + id + ".w.test":        {Id: id, Port: 3000},
		strings.ToUpper(id) + ".W.TEST": {Id: id},
	} {
		got, under, ok := application.ParseWorkspaceHost(host, "w.test")
		assert.True(t, under && ok, host)
		assert.Equal(t, want, got, host)
	}
	for _, host := range []string{"w.test", "x.w.test", "0-" + id + ".w.test", "03000-" + id + ".w.test", "65536-" + id + ".w.test", "a." + id + ".w.test"} {
		_, under, ok := application.ParseWorkspaceHost(host, "w.test")
		assert.True(t, under, host)
		assert.False(t, ok, host)
	}
	for _, host := range []string{"evilw.test", id + ".w.test.evil", "git.example.com"} {
		_, under, _ := application.ParseWorkspaceHost(host, "w.test")
		assert.False(t, under, host)
	}
	_, under, _ := application.ParseWorkspaceHost(id+".w.test", "")
	assert.False(t, under, "off without a domain")
}
