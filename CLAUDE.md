# CLAUDE.md

Guidance for Claude Code in this repository.

## What this is

oGit: a Git Smart HTTP server with users, sessions, per-repository access control and pluggable repository storage (local filesystem or S3-compatible). Go module `github.com/labbs/ogit`. Development happens on the `refactor` branch; `main` holds an older, unrelated layout (`internal/`, `pkg/`).

## Commands

```bash
go build -o ogit ./cmd/cmd.go                 # CGO is required (gorm sqlite driver)
./ogit migration -c config.yaml                # must run before the server; the server does not migrate
./ogit server -c config.yaml
air                                            # live reload: builds tmp/main, runs start.sh (migration + server)

go test -race ./...                            # everything, including end-to-end tests on fake S3; no Docker
go test -race -tags=integration -run MinIO ./...   # MinIO variants via testcontainers (Docker must be running)
go test -run 'TestGitHTTPAuthorization/s3_fake/non_member' ./interfaces/cli/server/   # single subtest
```

There is no Makefile. CI (`.github/workflows/pr.yaml`, PRs into `main` and `refactor`) runs gofmt, `go vet`, `go test -race`, a static Linux build, and a separate MinIO job. Keep `gofmt -l .` empty.

## Layout

```
cmd/cmd.go                         urfave/cli root: `server` and `migration` subcommands
interfaces/cli/server/server.go    runServer = setupDeps + cron jobs + routes + Listen
                                   setupDeps wires everything; tests call it directly
interfaces/http/                   SetupRoutes: /api/health, git routes, /api/v1 (fiber-oapi)
interfaces/http/git/               git Smart HTTP handlers + Authorize middleware (auth.go)
interfaces/http/v1/{auth,user,repository}/   API handlers and DTOs
application/                       business logic: AuthApp (login, register, AuthorizeGit),
                                   RepositoryApp, TokenApp, SessionApp, UserApp
domain/                            entities and persistence interfaces
infrastructure/config/             config.Config struct + one *_flags.go per section
infrastructure/persistence/        GORM implementations of the domain interfaces
infrastructure/migration/files/    goose migrations (Go files, one per table)
infrastructure/storage/            storage.Config interface (GetStorer, CreateRepository, ...)
  local/  s3/                      backends; s3/s3test = test endpoints (fake + MinIO)
```

## Configuration

`config.Config` is a struct filled by the flags in `infrastructure/config/*_flags.go` and passed by value. Each flag resolves CLI > env var > YAML (`-c`, default `config.yaml`; a missing file is silently ignored). `config-example.yaml` documents every key, and `./ogit server --help` lists the flags and env vars.

To add a setting: add a field to `config.Config`, add a flag with `cli.EnvVar(...)` and `altsrcyaml.YAML(...)` sources to the matching `*_flags.go`, and include that function in `getFlags` in `interfaces/cli/server/server.go` (and in the migration command if it needs it). Flag names and aliases must be unique; `TestFlagNamesAreUnique` enforces this.

Startup rules: `session.secret_key` must be ≥ 32 bytes, or empty (a random per-process key is generated, with a warning). Only `sqlite` and `postgres` dialects, and only `local` and `s3` storage, are implemented, although flag help mentions mysql and gcs.

## Auth model

- **API sessions**: login returns a JWT (HS256 only) that references a `session` row. fiber-oapi validates it on every `/api/v1` route unless `Security: "disabled"` is set (login, register, health).
- **Git over HTTP**: HTTP Basic, username = token name, password = personal access token (`POST /api/v1/user/token`). Tokens are `ogit_<64 hex>`. Only `application.HashToken` (SHA-256) is stored; lookups hash the presented value. Token types: `user` (scoped by repository roles) and `repository` (bound to one repo).
- `AuthApp.AuthorizeGit` decides, and `interfaces/http/git/auth.go` maps the result: `ErrGitUnauthenticated` → 401 + `WWW-Authenticate` (git prompts), `ErrGitNotFound` → 404, `ErrGitForbidden` → 403. Anonymous callers may only read public repos.
- **Never disclose private repositories**: callers who can't read one get the same 404 as for a missing repo (git routes and `RepositoryApp.GetRepositoryForUser`). Keep that for new endpoints.
- Roles (`domain/member.go`): reporter = read, developer = write, maintainer/owner = admin. The repo creator becomes owner. There are no endpoints yet to manage members or groups.

## Storage

Repositories are addressed by slug (`slug.Make(name)`, which also prevents path traversal). Git URLs are `/<slug>.git`, and the `/:repo` routes match one path segment only (no nested repos).

- **local**: `<path>/<slug>.git`, created by committing a README in a temp repo and cloning it bare, on `main`.
- **s3**: keys `repositories/<slug>.git/{config,HEAD,refs/...,objects/xx/...}`. The repo exists if `HEAD` exists. `S3Storer` is a hand-written go-git `storer.Storer` storing every object loose (no packfiles), so it is slow on big repos. The client uses path-style addressing and `WHEN_REQUIRED` checksums for non-AWS S3.

## Gotchas

- **fiber-oapi**: unauthenticated `/api/v1` requests get **400** `"authentication required"`, not 401. Its validator is a private global, so custom tags such as `slug` cannot be registered (an unknown tag makes every request 500).
- **GORM `default:` tags** drop zero values on insert, so the column default wins. This made every repo private (`IsPrivate` had `default:true`). Don't put defaults on booleans that callers set to false; apply defaults in the API layer instead (`is_private` is `*bool`, defaulting to true).
- **IDs** are set in application code (`utils.UUIDv4()`) or in `BeforeCreate`. An empty string ID inserts fine on SQLite and then breaks the next insert.
- **Enum strings** in `domain` must match the migration's CHECK/enum values (`TokenRepo` = `"repository"`).
- **Logging**: never log tokens, JWTs or passwords. `GormLogger.ParamsFilter` keeps bound values out of SQL logs; keep it.
- `helperError.Catch` maps any unique-constraint failure to `gorm.ErrDuplicatedKey`, so "already exists" messages can hide a different constraint.
- `.dev/*.sh` are curl helpers for a running server; their password satisfies the default policy.

## Tests

- End-to-end tests live in `interfaces/cli/server/*_test.go`. `startTestServer(t, opts...)` builds the real app (sqlite in a temp dir, migrations, routes, random port). Helpers: `registerAndLogin`, `createRepo`, `createToken`, `post`/`get`, `clone`, `commitAndPush` (go-git client).
- `testGitHTTPAuthorization` runs on local storage, fake S3 and MinIO (integration tag). New git behaviour should go in there so it is checked on every backend.
- S3 test endpoints come from `infrastructure/storage/s3/s3test`: `StartFake` (gofakes3), `StartMinIO` (`//go:build integration`), `Configure(cfg, ep)`, `NewStorage`. Tests in package `s3` must use package `s3_test` (`s3test` imports `s3`).
- MinIO image: `cgr.dev/chainguard/minio` pinned by digest, because `minio/minio` is no longer pullable from Docker Hub or quay.io.
- Pinned test deps: gofakes3 v1.0.0, testcontainers-go v0.40.0, grpc-gateway/v2 v2.16.0. Newer ones (or `go mod tidy` resolving @latest) can raise the `go` directive and upgrade the AWS SDK. Check `go.mod` after any `go get`/`tidy`.
- For security fixes, check that reverting the fix makes the test fail on an assertion, not on `log.Fatal` or a build error.

## Docker

The release workflow builds a static binary (`CGO_ENABLED=1`, `-extldflags -static`) into `bin/app`. The Dockerfile copies it into Alpine, and `entrypoint.sh` (POSIX `sh`, since Alpine has no bash) runs `migration` and then `server` with `/config/config.yaml`.
