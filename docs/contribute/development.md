# Development

## Setup

You need Go (the version in `go.mod`), a C toolchain (the SQLite driver uses CGO), Node.js (the version in `web/.nvmrc`), git, and Docker for the integration tests.

```bash
git clone https://github.com/svnix-solutions/ogit.git && cd ogit
npm --prefix web ci && npm --prefix web run build   # the web UI, embedded by go build
go build -o ogit ./cmd/cmd.go
./ogit migration -c config.yaml && ./ogit server -c config.yaml
```

For day-to-day work:

```bash
air                                # rebuilds and restarts the server on changes (.air.toml, start.sh)
npm --prefix web run dev           # the UI on :5173 with hot reload, proxying /api to :8080
npm --prefix docs run dev          # this site, with hot reload
docker compose up db               # only PostgreSQL, on :5432
```

## Tests

```bash
go test -race ./...                                     # unit and end-to-end tests: fake S3, no Docker
go test -race -tags=integration -run MinIO ./...        # the storage suites against MinIO
go test -race -tags=integration -run Mailpit ./...      # real SMTP through Mailpit
go test -race -tags=integration -run Redis ./...        # the shared Redis cache
OGIT_TEST_DB=postgres go test -race -tags=integration ./interfaces/cli/server/   # the end-to-end suite on PostgreSQL
npm --prefix web run typecheck && npm --prefix web test  # the web UI (vitest)
npm --prefix docs run build                              # the docs: dead links fail it
```

- **End-to-end tests** (`interfaces/cli/server/*_test.go`) start the real server with `startTestServer`: SQLite in a temporary directory, migrations, routes, a random port. Behaviour that touches git goes in the suites that run on every storage backend (local, fake S3, MinIO).
- **Use the real git client** for git protocol changes (the `gitCLI` helper, over HTTP and SSH): go-git clients and small repositories miss problems real pushes hit.
- **Test the rules:** each rule in an area's header comment has a test. Break the rule on purpose, and check that the test fails on an assertion, not a build error or a crash.
- **Run the PostgreSQL suite** for anything touching persistence or migrations: PostgreSQL enforces foreign keys and types that SQLite doesn't.

## Conventions

- **Formatting:** `gofmt -l .` must print nothing.
- **Dependencies are pinned** where newer versions would raise the `go` directive or upgrade the AWS SDK or x/crypto. After any `go get` or `go mod tidy`, check that `go.mod` still says `go 1.25.1`.
- **Settings:** a new setting is a field in `config.Config`, a flag with environment and YAML sources in `infrastructure/config/*_flags.go`, and an entry in `config-example.yaml`.
- **New sensitive endpoints** call `RequireSudo` first; new endpoints answer 404 for repositories the caller can't read.
- **Document features here, in `docs/`,** not in the README.

## Sending a change

1. Branch from `main`.
2. Make the change with its tests and docs.
3. Open a pull request into `main`. CI runs the web UI checks, gofmt, `go vet`, the tests with the race detector, the UI and static Linux builds, and the MinIO, Mailpit, Redis and PostgreSQL suites.
4. `main` is protected: it takes changes only through pull requests whose checks pass. Merged branches are deleted automatically.

Docs changes publish the site when they reach `main`. Releases are made by pushing a `v*` tag, which builds and publishes the container image.
