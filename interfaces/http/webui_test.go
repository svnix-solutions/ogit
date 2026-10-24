package http

import (
	"io"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWebUI(t *testing.T) {
	app := fiber.New()
	app.Get("/api/health", func(c *fiber.Ctx) error { return c.SendString("ok") })
	setupWebUI(app, fstest.MapFS{
		"index.html":          {Data: []byte("<html>app</html>")},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
		"favicon.svg":         {Data: []byte("<svg/>")},
		"assets/sub/deep.css": {Data: []byte("body{}")},
		"not-in-assets.txt":   {Data: []byte("plain")},
	})
	get := func(method, path string) (int, string, map[string][]string) {
		resp, err := app.Test(httptest.NewRequest(method, path, nil))
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), resp.Header
	}

	status, body, h := get("GET", "/repos/demo/tree/main")
	assert.Equal(t, 200, status)
	assert.Equal(t, "<html>app</html>", body, "app routes get index.html")
	assert.Equal(t, "no-cache", h["Cache-Control"][0])
	assert.Contains(t, h["Content-Security-Policy"][0], "frame-ancestors 'none'")

	status, body, h = get("GET", "/assets/index-abc.js")
	assert.Equal(t, 200, status)
	assert.Equal(t, "console.log(1)", body)
	assert.Contains(t, h["Cache-Control"][0], "immutable")
	assert.Contains(t, h["Content-Type"][0], "javascript")

	status, body, _ = get("GET", "/favicon.svg")
	assert.Equal(t, 200, status)
	assert.Equal(t, "<svg/>", body)

	status, _, _ = get("GET", "/assets/index-old.js")
	assert.Equal(t, 404, status, "a missing asset is not answered with index.html")

	status, body, _ = get("GET", "/assets/../index.html")
	assert.Equal(t, 200, status)
	assert.Equal(t, "<html>app</html>", body)
	status, body, _ = get("GET", "/../../etc/passwd")
	assert.Equal(t, 200, status, "paths are cleaned and stay inside the build")
	assert.Equal(t, "<html>app</html>", body)

	status, body, _ = get("GET", "/api/nope")
	assert.Equal(t, 404, status)
	assert.Contains(t, body, "NOT_FOUND")
	status, body, _ = get("GET", "/api/health")
	assert.Equal(t, "ok", body, "registered routes come first")

	status, _, _ = get("POST", "/somewhere")
	assert.Equal(t, 404, status, "only GET and HEAD")
	status, _, _ = get("HEAD", "/somewhere")
	assert.Equal(t, 200, status)
}

func TestWebUINotBuilt(t *testing.T) {
	app := fiber.New()
	setupWebUI(app, fstest.MapFS{".gitkeep": {}})
	resp, err := app.Test(httptest.NewRequest("GET", "/", nil))
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Contains(t, string(body), "npm --prefix web")
}
