// Package repopath lets the repository routes (/:owner/:repo/..., one
// namespace segment) serve repositories of sub-groups
// (/org/team/app.git/...): Middleware finds the repository path in the URL
// and routes the request as /~nested/<repo>/..., and handlers read the
// path with Path.
package repopath

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/labbs/ogit/domain"
)

const (
	local = "ogit.repo_path"
	// Placeholder stands for a nested namespace in rewritten paths; it is
	// no valid name, so a request can't use it directly.
	Placeholder = "~nested"
)

// Path is the repository path (<namespace>/<slug>, without .git) of a
// repository route.
func Path(ctx *fiber.Ctx) string {
	if p, ok := ctx.Locals(local).(string); ok {
		return p
	}
	return ctx.Params("owner") + "/" + strings.TrimSuffix(ctx.Params("repo"), ".git")
}

// Nested reports the nested repository path a request was rewritten for.
func Nested(ctx *fiber.Ctx) (string, bool) {
	p, ok := ctx.Locals(local).(string)
	return p, ok
}

type base struct {
	prefix  string
	markers map[string]bool // what follows the repository
	bare    bool            // the repository alone is a route
}

var bases = []base{
	{"/api/v3/repos/", set("statuses", "commits", "releases", "actions", "tarball", "zipball"), true},
	{"/api/uploads/repos/", set("releases"), false},
	{"/", set("info", "git-upload-pack", "git-receive-pack", "raw", "archive", "releases", "actions"), false},
}

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Middleware rewrites repository routes of sub-groups. groups returns
// which of the paths are groups.
func Middleware(groups func(paths []string) ([]string, error)) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		path := ctx.Path()
		for _, b := range bases {
			rest, ok := strings.CutPrefix(path, b.prefix)
			if !ok || (b.prefix == "/" && strings.HasPrefix(rest, "api/")) {
				continue
			}
			segs := strings.Split(rest, "/")
			if len(segs) < 3 {
				break // <namespace>/<repo>: the usual routes
			}
			// The deepest group among the leading segments: the repository
			// is the next one. A one-segment namespace takes the usual
			// routes.
			var prefixes []string
			for i := 2; i <= min(len(segs)-1, domain.MaxGroupDepth); i++ {
				prefixes = append(prefixes, strings.Join(segs[:i], "/"))
			}
			found, err := groups(prefixes)
			if err != nil || len(found) == 0 {
				break
			}
			depth := 0
			for _, g := range found {
				depth = max(depth, strings.Count(g, "/")+1)
			}
			after := segs[depth+1:]
			if (len(after) == 0 && !b.bare) || (len(after) > 0 && !b.markers[after[0]]) {
				break
			}
			ctx.Locals(local, strings.Join(segs[:depth], "/")+"/"+strings.TrimSuffix(segs[depth], ".git"))
			ctx.Path(b.prefix + strings.Join(append([]string{Placeholder, segs[depth]}, after...), "/"))
			break
		}
		return ctx.Next()
	}
}
