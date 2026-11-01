// Package wsagent is `ogit workspace-agent`: it reports to the server's agent
// protocol (/api/workspaces/agent) and runs workspaces in Docker.
package wsagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labbs/ogit/application"
)

// Client calls the agent protocol.
type Client struct {
	URL   string // the server's base URL
	Token string // the agent's token (ogita_...)
	HTTP  *http.Client
}

// ErrUnregistered: the server doesn't know the agent's token.
var ErrUnregistered = errors.New("the server no longer knows this agent (it was deleted): register it again")

func (c *Client) post(ctx context.Context, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+"/api/workspaces/agent"+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return resp.StatusCode, fmt.Errorf("server: %s", e.Message)
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}

// Registration is the server's answer to a registration.
type Registration struct {
	Id    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token"`
}

// Register turns a registration token into an agent token.
func (c *Client) Register(ctx context.Context, registrationToken, name, version string) (*Registration, error) {
	var r Registration
	if _, err := c.post(ctx, "/register", map[string]string{"token": registrationToken, "name": name, "version": version}, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Sync reports and gets what to do.
func (c *Client) Sync(ctx context.Context, report application.AgentReport) (*application.AgentTasks, error) {
	var t application.AgentTasks
	status, err := c.post(ctx, "/sync", report, &t)
	if status == http.StatusUnauthorized {
		return nil, ErrUnregistered
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
