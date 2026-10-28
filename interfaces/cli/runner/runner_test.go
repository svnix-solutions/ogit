package runner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
