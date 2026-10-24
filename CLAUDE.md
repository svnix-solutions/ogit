# CLAUDE.md

This file provides guidance to Claude Code when working in this repository.

## What this is

oGit: a Git server written in Go. It speaks Git Smart HTTP (and experimental SSH), and stores bare repositories either on the local filesystem or in an S3-compatible bucket. The Git protocol work is delegated to `go-git`'s `transport/server`. This project supplies the transports (Fiber HTTP, `x/crypto/ssh`) and the storage backends.

- Go module: `github.com/labbs/git-server-s3` (the repo dir is `ogit`, but the module and binary use the old name)
- Go 1.24 (`go.mod`). The Dockerfile's `golang:1.23` stage only copies a prebuilt binary and doesn't compile anything.

## Commands

```bash
make build                                 # -> tmp/git-server-s3 (entrypoint is cmd/cmd.go)
make run                                   # build + run with -c config.yaml; "server" is the only subcommand
air                                        # hot reload, uses .air.toml (builds cmd/cmd.go, runs with -c ./config.yaml)

go test ./...                              # all tests, including cmd/integration_test.go
go test ./pkg/... ./internal/...           # what `make test` / `make test-unit` run (adds -race; excludes ./cmd)
go test ./internal/api/controller/ -run TestCreateRepoSuccess -v   # single test
go test ./cmd/ -run TestLocalStorageIntegration -v                 # end-to-end over local storage
go test ./cmd/ -bench=. -benchmem
make test-integration                      # -tags=integration ./... : adds the MinIO variants (Docker must be running)

make fmt vet ci                            # ci = fmt + vet + test-unit
.dev/create-repo.sh                        # POST /api/repo {"name":"new-repo"} against localhost:8080
```

CI builds with `CGO_ENABLED=1 GOOS=linux go build ... -X main.version=... -o bin/app cmd/cmd.go`.
## Configuration

All config is held in package-level globals in `internal/config/config.go`. Flag definitions in `internal/flags/*.go` write into those globals through `Destination:`. Each flag value resolves in this order: CLI flag > env var > YAML key (via `urfave/cli-altsrc`, reading the file at `config.ConfigFile`, flag `-c/--config`, default `config.yaml`) > default.

| YAML key | Env | Default |
|---|---|---|
| `http.port` / `http.logs` | `HTTP_PORT` / `HTTP_LOGS` | 8080 / false |
| `ssh.enabled` / `ssh.port` / `ssh.hostkey` | `SSH_ENABLED` / `SSH_PORT` / `SSH_HOST_KEY_PATH` | false / 2222 / `./ssh_host_key` |
| `debug.endpoints` | `DEBUG_ENDPOINTS` | false |
| `logger.level` / `logger.pretty` | `LOGGER_LEVEL` / `LOGGER_PRETTY` | info / false |
| `storage.type` | `STORAGE_TYPE` | none. It must be `local` or `s3`, otherwise startup fails |
| `storage.local.path` | `STORAGE_LOCAL_PATH` | none (required for local) |
| `storage.s3.{bucket,endpoint,access-key,secret-key,region}` | `STORAGE_S3_*` | none |

Start from `config-example.yaml`. `config.yaml`, `repositories/`, `ssh_host_key` and `tmp/` are gitignored. In Docker, `entrypoint.sh` runs `./app server -c /config/config.yaml`.

To add a setting: add a field to the struct in `internal/config`, add a flag with an env var and YAML source in the matching `internal/flags/*.go`, and make sure it's included by `getFlags()` in `internal/cmd/cmd.go`.

Gotchas:
- Flag aliases must be unique across all `internal/flags/*.go` files (`-c`, `-p` and `-l` are taken). urfave/cli doesn't reject duplicates; they silently collide.
- The root command's `Before` hook in `cmd/cmd.go` (which appends `<config>.yaml`) is effectively dead code: `--config` is defined only on the `server` subcommand. Config loading actually goes through altsrc.
- Storage code reads `config.Storage.*` globals directly. Tests set those globals before they construct storage.

## Architecture

```
cmd/cmd.go                 urfave/cli v3 root -> internal/cmd.NewInstance ("server")
internal/cmd/cmd.go        runServer: build storage, Configure(), start HTTP (+ SSH) goroutines,
                           wait for SIGINT/SIGTERM, shut down with a 30s timeout
internal/server/http.go    Fiber app, middleware (recover, cors, compress, requestid, optional
                           zerolog access log), GET /health, then router.Config.Configure()
internal/api/router/       route registration only
internal/api/controller/   handlers: GitController (smart HTTP), RepoController (REST), DebugController
internal/server/git_ssh_*  ACTIVE SSH server (golang.org/x/crypto/ssh), wrapped by GitSSHConfig
internal/server/ssh.go     LEGACY gliderlabs/ssh SSHConfig: not wired into runServer, still
                           referenced by ssh_test.go
pkg/storage/git.go         GitRepositoryStorage interface + factory + GitServerLoader (go-git server.Loader)
pkg/storage/local/         LocalStorage: go-git filesystem storer over <path>/<repo>.git
pkg/storage/s3/            S3Storage + S3Storer (a hand-written go-git storer.Storer over S3 objects)
pkg/common/                repo-path helpers, pkt-line service advertisement, GetTransportServer
pkg/logger/                zerolog setup (+ unused gocron/goose adapters, HTTP logger middleware)
```

### Request flow (HTTP)

`GET /:repo/info/refs?service=git-upload-pack|git-receive-pack`, `POST /:repo/git-upload-pack` and `POST /:repo/git-receive-pack` go through `common.ExtractRepoPathFromURL`, then `common.GetTransportServer`. That function normalizes the path to `*.git`, returns 404 if the repo doesn't exist, and wraps the storage in a `GitServerLoader`. Next comes a go-git `NewUploadPackSession`/`NewReceivePackSession`, and the handler encodes the result straight into the Fiber response. The advertisement is prefixed by `common.WriteServiceAdvertisement`.

- `:repo` matches only **one path segment**. Nested repos (`org/project.git`) work at the storage layer and in the `pkg/common` helpers, but not over the HTTP routes.
- Pushing doesn't create repos. Create them first with `POST /api/repo {"name": "..."}`. `GET /api/repos` lists them.
- The debug routes (`/debug/memory`, `POST /debug/gc`, `/debug/goroutines[/stats]`) are registered only when `debug.endpoints` is true.

### Storage backends

The `GitRepositoryStorage` interface has `GetStorer`, `CreateRepository`, `RepositoryExists`, `DeleteRepository`, `ListRepositories` and `Configure`. To add a backend, implement it and add a case to `NewGitRepositoryStorage` in `pkg/storage/git.go`.

- **local**: `CreateRepository` makes a temporary non-bare repo with a README commit, then clones it bare into place.
- **s3**: the key layout is `repositories/<name>.git/{config,HEAD,refs/...,objects/...}`. A repo exists if `<prefix>/HEAD` exists. `CreateRepository` writes the initial blob, tree and commit objects plus `refs/heads/main` and HEAD itself. The client forces path-style addressing and sets `AWS_REQUEST_CHECKSUM_CALCULATION`/`AWS_RESPONSE_CHECKSUM_VALIDATION=WHEN_REQUIRED` so it works with non-AWS S3 (e.g. Outscale, MinIO). `S3Storer` stores every object loose, one S3 object per object, with no packfiles, so it's slow on large repos.
- `pkg/storage/storage.go` (`Storage`) and `pkg/storage/local/local.go` (`LocalConfig`) are older config helpers that are no longer used. The backends' own `Configure()` methods are the ones that run.

### SSH

SSH is marked as unstable in README_SSH.md (clients report "remote end hung up"). Prefer HTTP. **Authentication accepts any password and any public key** (`GitSSHServer.Configure`), whatever the README says about the password `demo`. The host key is an ed25519 key, generated at `ssh.hostkey` if it's missing.

## Tests

- `internal/api/controller/repo_controller_test.go`: Fiber + a testify `MockGitRepositoryStorage`. Reuse this mock when you add controller tests.
- `cmd/integration_test.go`: full HTTP app over local storage in a temp dir.
- `cmd/git_http_test.go`: real Git over HTTP (Fiber on a random port, go-git client): clone → commit → push → fresh clone → push a new branch. `testGitOverHTTP` runs against local storage (baseline) and fake S3; `git_http_integration_test.go` adds MinIO.
- `pkg/storage/s3/storage_test.go`: `testStorageSuite` covers S3Storage + S3Storer (create/exists/list/delete, initial commit, refs, object round-trip, CheckAndSetReference) against fake S3; `storage_integration_test.go` reruns it on MinIO.
- S3 test endpoints come from `internal/testutil/s3test`: `StartFake` (gofakes3 in-memory, plain `go test`) and `StartMinIO` (testcontainers, `//go:build integration` only, image `cgr.dev/chainguard/minio:latest` because `minio/minio` is no longer pullable from Docker Hub or quay.io). `NewStorage` sets the `config.Storage` globals and restores them on cleanup. Tests in `pkg/storage/s3` must use package `s3_test`, because `s3test` imports `s3`.
- Both backends create new repositories on `main` (`plumbing.Main`), and `testGitOverHTTP` asserts it. Repos created on local storage before this change are on `master`.
- Dependency pins: test deps are held at `gofakes3 v1.0.0`, `testcontainers-go v0.40.0` and `grpc-gateway/v2 v2.16.0`. Newer versions (or `go mod tidy` resolving them @latest) raise the module's `go` directive to 1.25/1.26 and upgrade the AWS SDK. Check `go.mod`'s `go` line after any `go get`/`tidy`.
- `internal/server/ssh_test.go`: tests the **legacy** `SSHConfig`, not the active `GitSSHServer`.
- `pkg/common/common_test.go`: expects `NormalizeRepoPath` to strip leading/trailing slashes (`"/test-repo/"` → `"test-repo.git"`), but the implementation only trims whitespace and appends `.git`. These cases are known to fail (TESTS.md says so). Decide whether to fix the code or the test before you touch either one.

TESTS.md and pkg/storage/README.md are in French. Some of their references (`examples/...`) point to files that don't exist.
