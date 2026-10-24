//go:build integration

package mailtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const mailpitImage = "axllent/mailpit:v1.27.10"

// Mailpit is a running Mailpit container: SMTP on Host:SMTPPort (no TLS, no
// auth) and its HTTP API at APIURL.
type Mailpit struct {
	Host     string
	SMTPPort int
	APIURL   string
}

// StartMailpit starts Mailpit (requires Docker); it is removed when the test ends.
func StartMailpit(t testing.TB) *Mailpit {
	t.Helper()
	ctx := context.Background()

	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        mailpitImage,
			ExposedPorts: []string{"1025/tcp", "8025/tcp"},
			WaitingFor:   wait.ForHTTP("/api/v1/info").WithPort("8025/tcp"),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)

	host, err := ctr.Host(ctx)
	require.NoError(t, err)
	smtpPort, err := ctr.MappedPort(ctx, "1025/tcp")
	require.NoError(t, err)
	apiPort, err := ctr.MappedPort(ctx, "8025/tcp")
	require.NoError(t, err)

	return &Mailpit{Host: host, SMTPPort: smtpPort.Int(), APIURL: fmt.Sprintf("http://%s:%s", host, apiPort.Port())}
}

// Messages returns the messages delivered to address, newest first.
func (m *Mailpit) Messages(t testing.TB, address string) []Message {
	t.Helper()
	var list struct {
		Messages []struct {
			ID      string
			Subject string
			To      []struct{ Address string }
		}
	}
	m.getJSON(t, "/api/v1/search?query="+url.QueryEscape("to:"+address), &list)

	var out []Message
	for _, s := range list.Messages {
		var full struct{ Text string }
		m.getJSON(t, "/api/v1/message/"+s.ID, &full)
		out = append(out, Message{To: address, Subject: s.Subject, Body: full.Text})
	}
	return out
}

func (m *Mailpit) getJSON(t testing.TB, path string, v any) {
	t.Helper()
	resp, err := http.Get(m.APIURL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "mailpit %s", path)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
}
