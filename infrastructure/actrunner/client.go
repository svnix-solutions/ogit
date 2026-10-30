// Package actrunner is `ogit runner`: it talks to the server's runner
// protocol (/api/actions/runner) and runs workflows with act, in Docker.
package actrunner

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

// Client calls the runner protocol.
type Client struct {
	URL   string // the server's base URL
	Token string // the runner's token (ogitr_...)
	HTTP  *http.Client
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *Client) do(ctx context.Context, path string, body []byte, contentType string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+"/api/actions/runner"+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Message string }
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = resp.Status
		}
		return resp.StatusCode, fmt.Errorf("server: %s", e.Message)
	}
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) json(ctx context.Context, path string, in, out any) (int, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	return c.do(ctx, path, body, "application/json", out)
}

// Registration is the server's answer to a registration.
type Registration struct {
	Id        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	Scope     string `json:"scope"`
	Ephemeral bool   `json:"ephemeral"`
}

// ErrUnregistered: the server doesn't know the runner's token (it was
// deleted, or removed after a day offline).
var ErrUnregistered = errors.New("the server no longer knows this runner (deleted, or removed after a day offline): register it again")

// Register turns a registration token into a runner token. An ephemeral
// runner takes one run.
func (c *Client) Register(ctx context.Context, registrationToken, name string, labels []string, version string, ephemeral bool) (*Registration, error) {
	var r Registration
	_, err := c.json(ctx, "/register", map[string]any{"token": registrationToken, "name": name, "labels": labels, "version": version, "ephemeral": ephemeral}, &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// Fetch waits up to wait for a run (nil: none).
func (c *Client) Fetch(ctx context.Context, labels []string, version string, wait time.Duration) (*application.Task, error) {
	var t application.Task
	status, err := c.json(ctx, "/fetch", map[string]any{"labels": labels, "version": version, "wait": int(wait.Seconds())}, &t)
	if status == http.StatusUnauthorized {
		return nil, ErrUnregistered
	}
	if err != nil || status == http.StatusNoContent {
		return nil, err
	}
	return &t, nil
}

// State reports a run's progress; cancel: stop it.
func (c *Client) State(ctx context.Context, runId string, u application.StateUpdate) (cancel bool, err error) {
	var out struct{ Cancel bool }
	_, err = c.json(ctx, "/runs/"+runId+"/state", u, &out)
	return out.Cancel, err
}

// Log appends output to a run's log.
func (c *Client) Log(ctx context.Context, runId string, data []byte) error {
	_, err := c.do(ctx, "/runs/"+runId+"/log", data, "text/plain; charset=utf-8", nil)
	return err
}
