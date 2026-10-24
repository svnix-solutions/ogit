# oGit

A Git server over HTTP with users, access control and pluggable repository
storage (local filesystem or any S3-compatible object store).

## Features

- **Git Smart HTTP**: clone, fetch and push with any git client
- **Users and sessions**: registration and login with JWT sessions, plus a configurable registration policy
- **Access control**: public and private repositories, per-repository roles (owner, maintainer, developer, reporter), and personal access tokens with `read:repository` / `write:repository` scopes
- **Storage backends**: local filesystem, or S3-compatible object storage (AWS S3, MinIO, Outscale, ...)
- **Database**: SQLite or PostgreSQL
- **REST API** with OpenAPI documentation at `/documentation`

## Quick start

Building needs Go (see `go.mod`) and a C toolchain, because the SQLite driver uses CGO.

```bash
go build -o ogit ./cmd/cmd.go

cp config-example.yaml config.yaml   # then edit it
./ogit migration -c config.yaml      # create or upgrade the database schema
./ogit server -c config.yaml         # listens on :8080 by default
```

Run `migration` before the first start and after every upgrade. The server
starts without it, but API calls then fail with missing tables.

### Docker

Release images are published to `ghcr.io/<owner>/<repo>` for `v*` tags. The
container reads `/config/config.yaml`, runs the migrations, then starts the
server:

```bash
docker run -p 8080:8080 \
  -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -v "$PWD/data:/data" \
  ghcr.io/<owner>/<repo>:<tag>
```

Point `database.dsn` and `storage.local.path` at the `/data` volume, or use
PostgreSQL and S3. `docker-compose.yaml` starts a local PostgreSQL for
development.

## Using git

Accounts and repositories are managed through the API (`/documentation` lists
every endpoint):

```bash
# Register and log in; the login response contains a session token
curl -X POST localhost:8080/api/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"email": "me@example.com", "username": "me", "password": "Correct-Horse-9"}'
SESSION=$(curl -s -X POST localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"email": "me@example.com", "password": "Correct-Horse-9"}' | jq -r .token)

# Create a repository (private unless "is_private": false)
curl -X POST localhost:8080/api/v1/repository -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "My Project"}'

# Create a personal access token for git; the "token" field is shown only once
curl -X POST localhost:8080/api/v1/user/token -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' \
  -d '{"name": "laptop", "scopes": ["read:repository", "write:repository"]}'
```

Repositories are addressed by their slug (`My Project` → `my-project`). Git
asks for credentials: the username is the **token name** and the password is
the **token**.

```bash
git clone http://localhost:8080/my-project.git
```

| Caller | Clone / fetch | Push |
|---|---|---|
| Anonymous | public repositories only | never |
| Token with `read:repository` | public repos, and private repos the user can read | no |
| Token with `write:repository` | as above | repos where the user is developer or higher |

Private repositories that the caller can't read are reported as not found.

## Configuration

Settings come from the YAML file given with `-c` (default `config.yaml`; a
missing file is ignored), from environment variables, or from flags. Flags
override environment variables, and environment variables override the file.
`config-example.yaml` documents every key. The main ones:

| YAML key | Environment | Default |
|---|---|---|
| `http.port` | `HTTP_PORT` | `8080` |
| `http.logs` | `HTTP_LOGS` | `false` |
| `logger.level` / `logger.pretty` | `LOGGER_LEVEL` / `LOGGER_PRETTY` | `info` / `false` |
| `database.dialect` | `DATABASE_DIALECT` | `sqlite` (or `postgres`) |
| `database.dsn` | `DATABASE_DSN` | `./database.sqlite` |
| `session.secret_key` | `SESSION_SECRET_KEY` | random per start; set ≥ 32 bytes in production |
| `session.expiration_minutes` | `SESSION_EXPIRATION_MINUTES` | `43200` (30 days) |
| `registration.enabled` | `REGISTRATION_ENABLED` | `true` |
| `registration.domain_whitelist` | `REGISTRATION_DOMAIN_WHITELIST` | any domain |
| `registration.password_min_length` | `REGISTRATION_PASSWORD_MIN_LENGTH` | `12` |
| `registration.password_complexity` | `REGISTRATION_PASSWORD_COMPLEXITY` | `true` |
| `storage.type` | `STORAGE_TYPE` | `local` (or `s3`) |
| `storage.local.path` | `STORAGE_LOCAL_PATH` | `./repositories` |
| `storage.s3.bucket`, `.endpoint`, `.accesskey`, `.secretkey`, `.region` | `STORAGE_S3_*` | region `us-east-1` |

`./ogit server --help` lists every flag.

## Not implemented yet

- SSH access
- Logout, the user profile endpoint, and cleanup of expired sessions
- Email verification (`registration.require_email_verification` is not enforced)
- Endpoints for managing repository members, groups and SSH keys
- Repositories in nested paths (`org/project.git`)

## Development

```bash
go test -race ./...                            # unit and end-to-end tests (fake S3, no Docker)
go test -race -tags=integration -run MinIO ./...  # same suites against MinIO (needs Docker)
air                                            # live reload, see .air.toml and start.sh
```

Pull requests into `main` and `refactor` run gofmt, `go vet`, the tests and
the MinIO integration tests in CI.

## License

See [LICENSE](LICENSE).
