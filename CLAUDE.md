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
OGIT_TEST_DB=postgres go test -race -tags=integration ./interfaces/cli/server/   # whole e2e suite on Postgres
go test -race -tags=integration -run Mailpit ./...   # real SMTP via a Mailpit container
go test -race -tags=integration -run Redis ./...     # Redis cache, incl. two servers sharing it
go test -run 'TestGitHTTPAuthorization/s3_fake/non_member' ./interfaces/cli/server/   # single subtest
```

There is no Makefile. CI (`.github/workflows/pr.yaml`, PRs into `main` and `refactor`) runs gofmt, `go vet`, `go test -race`, a static Linux build, and a separate job running the MinIO and Mailpit tests and the e2e suite on Postgres. Keep `gofmt -l .` empty.

## Layout

```
cmd/cmd.go                         urfave/cli root: `server`, `migration`, `admin` and `storage repack|gc` subcommands
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

- **API sessions**: login returns a JWT (HS256 only) that references a `session` row. fiber-oapi validates it on every `/api/v1` route unless `Security: "disabled"` is set (login, register, health). `SessionApp.ValidateToken` puts the session ID into `AuthContext.Claims[application.SessionIDClaim]`. `POST /api/v1/auth/logout` deletes that row, so the JWT stops validating although it has not expired. A cron job (`infrastructure/jobs`) deletes expired sessions every minute.
- **Email verification** (`application/email_verification.go`, off by default): when `registration.require_email_verification` is on, `Authenticate` refuses unverified users (after the password check) with `ErrEmailNotVerified` (403). `setupDeps` refuses to start without `smtp.host`, `smtp.from` and `http.public_url`. Links are single-use and stored hashed, one pending per user (`email_verification`). Resend always answers 200 and is rate-limited. The link is a plain Fiber GET (HTML page), next to the `POST /api/v1/auth/verify` JSON endpoint. Mail goes through `mail.Sender`: `SMTPSender` in production; tests set `srv.deps.AuthApp.Mailer` to a `mailtest.Recorder` or use `mailtest.StartMailpit` (integration tag).
- **Git over HTTP**: HTTP Basic, username = token name, password = personal access token (`POST /api/v1/user/token`). Tokens are `ogit_<64 hex>`. Only `application.HashToken` (SHA-256) is stored; lookups hash the presented value. Token types: `user` (scoped by repository roles) and `repository` (bound to one repo).
- `AuthApp.AuthorizeGit` decides, and `interfaces/http/git/auth.go` maps the result: `ErrGitUnauthenticated` → 401 + `WWW-Authenticate` (git prompts), `ErrGitNotFound` → 404, `ErrGitForbidden` → 403. Anonymous callers may only read public repos.
- **Login never reveals whether an account exists**: an unknown email and a wrong password both return 401 `INVALID_CREDENTIALS` and both pay for one bcrypt comparison (`dummyPasswordHash`). Account state (disabled, unverified) is only reported after a correct password. Keep this for any new credential check. `TestLoginDoesNotRevealAccounts` checks the answer and the timing.
- **SSH keys** (`application/ssh_key_app.go`, rules at the top): only public keys are stored. Accepted: ed25519, ECDSA, RSA >= 3072, and FIDO sk- variants. Users manage their own keys (an email when one is added, if SMTP is configured); maintainers+ manage deploy keys; admins list and revoke.
- **Repository lifecycle** (`application/repository_lifecycle.go`): owners rename, change visibility, archive and delete (`?confirm=<slug>`); maintainers+ edit the description. Archived repositories refuse pushes (`ErrGitArchived`, checked after authorization) on HTTP and SSH. Owner and admin deletion share `purgeRepository` (rows, then git data).
- **Site admins** (`application/admin_app.go`, rules at the top): `AdminApp.RequireAdmin` re-reads the user on every call (role `admin` and active). Admins get no implicit repository access; `SetRepositoryOwner` makes it explicit. The API keeps at least one active admin (`ErrLastAdmin`). `ogit admin promote|demote` (`interfaces/cli/admin`) writes the role directly and is the bootstrap and recovery path. Deleting a user is refused with `OrphanError` (listing repos and groups) while they solely own a repo or own a group. `SessionApp.ValidateToken` rejects sessions of disabled users, and disabling also deletes their sessions; tests check each layer separately. Admin actions log `"component":"audit"`.
- **Never disclose private repositories**: callers who can't read one get the same 404 as for a missing repo (git routes and `RepositoryApp.GetRepositoryForUser`). Keep that for new endpoints.
- Roles (`domain/member.go`): reporter = read, developer = write, maintainer/owner = admin. The repo creator becomes owner. `RepositoryApp.GetUserEffectiveRole` is the max of the direct membership and every group membership (via `group_member`).
- **Membership rules** live in `application/repository_members.go` and `application/group_app.go` (each file starts with a summary). Owners do anything; maintainers are capped at developer (`checkCanManage`). Groups are at most maintainer on a repo and can only be added by their members. Repos keep at least one owner (`domain.ErrLastOwner`, enforced in `repositoryMemberPers` inside a transaction, with `FOR UPDATE` on postgres; `TestLastOwnerConcurrentDemotion` shows the lock is needed). Groups have exactly one owner (`group.owner_id` mirrors the owner's `group_member` row) and change owner only through `TransferOwnership`.
- Application errors (`application/errors.go`) map to HTTP statuses in one place, `interfaces/http/v1/apierror`. Use `apierror.From` in new handlers. Path params in request DTOs are tagged `json:"-"`, because fiber-oapi parses the body after the path and a body field could otherwise override them.

## Storage

Repositories are addressed by slug (`slug.Make(name)`, which also prevents path traversal). Git URLs are `/<slug>.git`, and the `/:repo` routes match one path segment only (no nested repos). **Storage is keyed by `repository.storage_key`, not the slug**: the id for new repositories, the original slug for those created before renames existed. Never pass a slug to `storage.Config`. `AuthorizeGit` / `AuthorizeSSH` return the resolved repository, and the HTTP git handlers read its storage key from `ctx.Locals(storageKeyLocal)`. Renames keep the old slug as a `repository_redirect` (resolved inside `RepositoryApp.GetRepository`, so it works for the API, HTTP and SSH); a repository created with that slug, or renamed back, takes it over.

- **local**: `<path>/<storage_key>.git`, created by committing a README in a temp repo and cloning it bare, on `main`.
- **s3**: keys `repositories/<storage_key>.git/{config,HEAD,refs/...,objects/pack/pack-<sha>.{pack,idx}}`. The repo exists if `HEAD` exists. The client uses path-style addressing and `WHEN_REQUIRED` checksums for non-AWS S3. `S3Storer` is a hand-written go-git `storer.Storer`:
  - **Objects** (`s3/packs.go`): `PackfileWriter` stages a pushed pack in a temp file, indexes it, uploads the `.pack` and then the `.idx` (an index means a complete pack). `state()` lists the packs once per storer, i.e. once per request, so servers sharing a bucket see each other's pushes. Packs are read by ranged GETs of 4 MiB blocks (`s3/blockfile.go`). New repositories are packed too (`createInitialCommit`).
  - **Loose objects** (`objects/xx/...`, the layout before packs) are still read, but only if the listing found some (`hasLoose`). `S3Storage.Repack` / `ogit storage repack` (`interfaces/cli/storage`) packs them, checks every hash is in the new pack, and only then deletes them (a request that listed before the new pack existed can then miss an object).
  - **GC** (`s3/gc.go`, `ogit storage gc`, command only): merges small packs geometrically (`mergeSplit`, factor 2, like `git repack --geometric`) and never prunes. Replaced packs get `pack-<sha>.drop`, which `state()` skips; a later run deletes `.idx`, `.pack`, then `.drop` once the marker is older than `--grace`. Indexless packs are deleted only after 24h (`unindexedAge`), whatever the grace, because a push in flight has its `.pack` before its `.idx`. No lock: each run drops packs only after its merged pack is uploaded and verified (`TestConcurrentGC`).
  - **Cache** (`infrastructure/storage/cache`, `storage.s3.cache.*`): `memory` LRU (default, 256 MiB), `redis` (shared; 250ms timeouts, no retries, skipped for 30s after an error so an outage costs nothing) or `none`. Keys are `bucket/key#block`. That is safe only because packs are immutable; never cache refs or HEAD.
  - **Refs are compare-and-swap**: `CheckAndSetReference` uses `If-Match` on the ETag, `CreateReference` uses `If-None-Match: *`. `GuardedReceivePack` (`helpers/git/receive_pack.go`) makes go-git's receive-pack pass the client's old value to them, which it otherwise ignores. MinIO (and gofakes3) ignore `If-Match` on DELETE, so `RemoveReferenceIf` compares first and a concurrent delete race remains there.
  - `TestS3RequestBudget` fails if a push, clone or fetch of a 300-commit repo needs more than 25 requests (counted by `s3test.StartCountingFake`).

## Gotchas

- **fiber-oapi**: unauthenticated `/api/v1` requests get **400** `"authentication required"`, not 401. Its validator is a private global, so custom tags such as `slug` cannot be registered (an unknown tag makes every request 500).
- **GORM `default:` tags** drop zero values on insert, so the column default wins. This made every repo private (`IsPrivate` had `default:true`). Don't put defaults on booleans that callers set to false; apply defaults in the API layer instead (`is_private` is `*bool`, defaulting to true).
- **IDs** are set in application code (`utils.UUIDv4()`) or in `BeforeCreate`. An empty string ID inserts fine on SQLite and then breaks the next insert.
- **Postgres differs from SQLite**: JSON columns come back as `[]byte`, not `string`. `sql.Scanner` implementations must handle string, []byte and nil and return errors. A panic inside `Scan` deadlocks the connection (`database/sql` still holds the rows lock), so the request hangs instead of failing. That is how every Postgres login hung (`JSONB.Scan`). Run the Postgres e2e suite for anything touching persistence or migrations.
- **Fetch negotiation and pushes are not go-git's** (`infrastructure/helpers/git/upload_pack.go`, `no_thin.go`). go-git's server does no negotiation, so `ServeUploadPack` implements it: plain protocol over SSH (one stateful stream), and `multi_ack_detailed` over HTTP. HTTP is stateless and a plain-mode client does not resend acknowledged commons; `AdvertiseUploadPackHTTP` advertises it. Receive-pack advertises `no-thin`: go-git cannot resolve thin packs ("reference delta not found"), which every real push against existing history produces. `TestFetchNegotiation` (300 commits, unpushed commits, unrelated history) and `TestPushDeltaAgainstExistingObjects` use the real git client and catch regressions that go-git clients and small test repos never hit.
- **Git over SSH** (`interfaces/ssh`): x/crypto/ssh server; user `git` only, key identified by SHA256 fingerprint (`ssh_key.fingerprint`, unique: user key or deploy key), then `AuthApp.IdentifySSHKey` / `AuthorizeSSH` mirror the HTTP rules. Unlike HTTP the stream stays open, so: send `exit-status` **before** closing the channel (otherwise git says "remote end hung up"); a client with nothing to fetch/push sends only a flush-pkt (`isFlush`); a delete-only push sends no pack, so `req.Packfile` must be nil (`onlyDeletes`) or the server blocks reading one. All three are covered by `TestGitOverSSH` with the real `git` client; keep testing SSH changes with the real CLI (`gitCLI` helper), not only go-git.
- **SQLite does not enforce foreign keys** here (the `foreign_keys` pragma is off), so `ON DELETE CASCADE` only works on postgres. Delete dependent rows explicitly in a transaction (see `groupPers.Delete`).
- **Enum strings** in `domain` must match the migration's CHECK/enum values (`TokenRepo` = `"repository"`).
- **Logging**: never log tokens, JWTs or passwords. `GormLogger.ParamsFilter` keeps bound values out of SQL logs; keep it.
- `helperError.Catch` maps any unique-constraint failure to `gorm.ErrDuplicatedKey`, so "already exists" messages can hide a different constraint.
- `.dev/*.sh` are curl helpers for a running server; their password satisfies the default policy.

## Tests

- End-to-end tests live in `interfaces/cli/server/*_test.go`. `startTestServer(t, opts...)` builds the real app (sqlite in a temp dir, migrations, routes, random port). Helpers: `registerAndLogin`, `createRepo`, `createToken`, `post`/`get`, `clone`, `commitAndPush` (go-git client).
- `testGitHTTPAuthorization` runs on local storage, fake S3 and MinIO (integration tag). New git behaviour should go in there so it is checked on every backend.
- S3 test endpoints come from `infrastructure/storage/s3/s3test`: `StartFake` (gofakes3), `StartMinIO` (`//go:build integration`), `Configure(cfg, ep)`, `NewStorage`. Tests in package `s3` must use package `s3_test` (`s3test` imports `s3`).
- MinIO image: `cgr.dev/chainguard/minio` pinned by digest, because `minio/minio` is no longer pullable from Docker Hub or quay.io.
- Pinned test deps: gofakes3 v1.0.0, testcontainers-go v0.40.0 (and its redis module), grpc-gateway/v2 v2.16.0, go-redis v9.22.0 (v9.23 needs Go 1.26). Newer ones (or `go mod tidy` resolving @latest) can raise the `go` directive and upgrade the AWS SDK. Check `go.mod` after any `go get`/`tidy`.
- For security fixes, check that reverting the fix makes the test fail on an assertion, not on `log.Fatal` or a build error.

## Docker

The release workflow builds a static binary (`CGO_ENABLED=1`, `-extldflags -static`) into `bin/app`. The Dockerfile copies it into Alpine, and `entrypoint.sh` (POSIX `sh`, since Alpine has no bash) runs `migration` and then `server` with `/config/config.yaml`.
