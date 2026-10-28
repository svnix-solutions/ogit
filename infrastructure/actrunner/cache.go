package actrunner

import (
	"context"
	"io"
	"path"
	"strings"

	docker_container "github.com/moby/moby/api/types/container"
	"github.com/nektos/act/pkg/runner"
)

// actionCache serves the runner's own actions (ogit/upload-artifact and
// ogit/download-artifact, see shims.go), then fetches `uses:` actions
// from this server (public repositories, or the run's own with its job
// token), then from github.com, anonymously, so marketplace actions keep
// working.
type actionCache struct {
	inner  runner.GoGitActionCache
	server string
}

const githubURL = "https://github.com"

func (a actionCache) Fetch(ctx context.Context, cacheDir, url, ref, token string) (string, error) {
	if name := shimFor(url); name != "" {
		return writeShim(path.Join(a.inner.Path, safeFilename(cacheDir)+".git"), name)
	}
	sha, err := a.inner.Fetch(ctx, cacheDir, url, ref, token)
	if err == nil || !strings.HasPrefix(url, a.server+"/") {
		return sha, err
	}
	return a.inner.Fetch(ctx, cacheDir, githubURL+strings.TrimPrefix(url, a.server), ref, "")
}

func (a actionCache) GetTarArchive(ctx context.Context, cacheDir, sha, includePrefix string) (io.ReadCloser, error) {
	return a.inner.GetTarArchive(ctx, cacheDir, sha, includePrefix)
}

func containerNetwork(name string) docker_container.NetworkMode {
	return docker_container.NetworkMode(name)
}
