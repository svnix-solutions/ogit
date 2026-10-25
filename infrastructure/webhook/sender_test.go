package webhook

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPermitted(t *testing.T) {
	s, err := NewSender([]string{"10.1.0.0/16", "192.168.5.5", "ci.internal"}, time.Second)
	require.NoError(t, err)
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.0.0.1": false, "172.16.3.4": false, "192.168.1.1": false,
		"169.254.169.254": false, "fe80::1": false, "fc00::1": false, "100.64.1.1": false, "0.0.0.0": false,
		"::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"10.1.2.3": true, "192.168.5.5": true, // allowed
	} {
		assert.Equal(t, want, s.permitted("example.com", net.ParseIP(ip)), ip)
	}
	assert.True(t, s.permitted("CI.internal", net.ParseIP("10.9.9.9")), "an allowed host name")
	assert.False(t, s.permitted("example.com", nil))
}

func TestValidateURL(t *testing.T) {
	assert.NoError(t, ValidateURL("https://ci.example.com/hook?x=1"))
	for _, bad := range []string{"ftp://x/y", "https:///nohost", "https://user:pw@x/", "javascript:alert(1)", "not a url"} {
		assert.Error(t, ValidateURL(bad), bad)
	}
}

func TestSend(t *testing.T) {
	var got *http.Request
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("thanks"))
	}))
	defer srv.Close()
	req := Request{URL: srv.URL + "/hook", Secret: "s3cret", Event: "push", DeliveryId: "d1", HookId: "h1", Payload: []byte(`{"ref":"refs/heads/main"}`)}

	blocked, err := NewSender(nil, time.Second)
	require.NoError(t, err)
	resp := blocked.Send(context.Background(), req)
	assert.ErrorIs(t, resp.Err, ErrBlockedAddress, "127.0.0.1 is blocked by default")
	assert.Nil(t, got, "nothing reached the server")

	s, err := NewSender([]string{"127.0.0.1"}, time.Second)
	require.NoError(t, err)
	resp = s.Send(context.Background(), req)
	require.NoError(t, resp.Err)
	assert.Equal(t, http.StatusAccepted, resp.Status)
	assert.Equal(t, "thanks", resp.Body)
	assert.Equal(t, `{"ref":"refs/heads/main"}`, string(body))
	assert.Equal(t, "push", got.Header.Get("X-GitHub-Event"))
	assert.Equal(t, "d1", got.Header.Get("X-GitHub-Delivery"))
	assert.Equal(t, "push", got.Header.Get("X-Ogit-Event"))
	assert.Equal(t, Signature("s3cret", body), got.Header.Get("X-Hub-Signature-256"))
	assert.Equal(t, "sha256=", Signature("s3cret", body)[:7])
	assert.NotEmpty(t, got.Header.Get("X-Hub-Signature"))

	req.URL = srv.URL + "/redirect"
	resp = s.Send(context.Background(), req)
	require.NoError(t, resp.Err)
	assert.Equal(t, http.StatusFound, resp.Status, "redirects are not followed")

	req.Secret = ""
	req.URL = srv.URL + "/hook"
	s.Send(context.Background(), req)
	assert.Empty(t, got.Header.Get("X-Hub-Signature-256"), "no secret, no signature")
}
