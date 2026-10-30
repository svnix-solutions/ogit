package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labbs/ogit/infrastructure/actrunner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// register runs `ogit runner register` against a fake server and returns
// the labels it sent and the state it saved.
func register(t *testing.T, args ...string) (sent []string, saved State) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/register"), r.URL.Path)
		var body struct{ Labels []string }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sent = body.Labels
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "r1", "name": "test", "token": "ogitr_x", "scope": "repository"})
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), ".runner")
	cmd := NewInstance("test")
	all := append([]string{"runner", "register", "--file", file, "--url", srv.URL, "--token", "ogitreg_x", "--name", "test"}, args...)
	require.NoError(t, cmd.Run(context.Background(), all))
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	return sent, saved
}

func TestRegisterLabels(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		sent, saved := register(t)
		assert.Equal(t, []string{"ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04"}, sent)
		assert.Equal(t, DefaultLabels, saved.Labels)
	})

	t.Run("given, comma-separated", func(t *testing.T) {
		sent, saved := register(t, "--labels", "ubuntu-latest:docker://node:20-bookworm,docker:docker://catthehacker/ubuntu:act-22.04")
		assert.Equal(t, []string{"ubuntu-latest", "docker"}, sent)
		assert.Equal(t, []string{"ubuntu-latest:docker://node:20-bookworm", "docker:docker://catthehacker/ubuntu:act-22.04"}, saved.Labels)
	})

	t.Run("given, repeated", func(t *testing.T) {
		_, saved := register(t, "--labels", "big:docker://my/image", "--labels", "small:docker://alpine:3")
		assert.Equal(t, []string{"big:docker://my/image", "small:docker://alpine:3"}, saved.Labels)
	})
}

func TestRegisterEphemeral(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Ephemeral bool }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		sent = body.Ephemeral
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "r1", "name": "test", "token": "ogitr_x", "scope": "repository", "ephemeral": body.Ephemeral})
	}))
	defer srv.Close()
	file := filepath.Join(t.TempDir(), ".runner")
	require.NoError(t, NewInstance("test").Run(context.Background(), []string{"runner", "register", "--file", file, "--url", srv.URL, "--token", "ogitreg_x", "--ephemeral"}))
	assert.True(t, sent)
	var saved State
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &saved))
	assert.True(t, saved.Ephemeral, "`run` reads it")
}

// A runner the server no longer knows stops, so that a supervisor can
// register it again.
func TestLoopStopsWhenUnregistered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid runner token"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := Loop(ctx, &actrunner.Client{URL: srv.URL, Token: "ogitr_x"}, nil, "test", actrunner.Options{}, false, io.Discard)
	assert.ErrorIs(t, err, actrunner.ErrUnregistered)
	assert.NoError(t, ctx.Err(), "at once")
}

// An ephemeral runner stops after one run.
func TestLoopOnce(t *testing.T) {
	var fetches int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/fetch") {
			mu.Lock()
			fetches++
			mu.Unlock()
			// A run that fails at once: no job for the event, no Docker needed.
			_ = json.NewEncoder(w).Encode(map[string]any{"run_id": "run1", "number": 1, "repository": "o/r", "workflow_path": ".ogit/workflows/ci.yml",
				"workflow": "on: pull_request\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: true\n", "event": "push", "event_payload": map[string]any{}})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	require.NoError(t, Loop(ctx, &actrunner.Client{URL: srv.URL, Token: "ogitr_x"}, nil, "test", actrunner.Options{WorkDir: t.TempDir()}, true, io.Discard))
	assert.NoError(t, ctx.Err())
	assert.Equal(t, 1, fetches)
}
