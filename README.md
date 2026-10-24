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

### Members and groups

The creator of a repository is its owner. Owners and maintainers share it
with other users or groups (`/documentation` lists every endpoint):

```bash
# Give a user access: reporter (read), developer (push), maintainer, owner
curl -X POST localhost:8080/api/v1/repository/my-project/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "alice", "role": "developer"}'

# Create a group, add people, then give the whole group access
curl -X POST localhost:8080/api/v1/group -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "backend"}'
curl -X POST localhost:8080/api/v1/group/backend/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "bob", "role": "member"}'
curl -X POST localhost:8080/api/v1/repository/my-project/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"group": "backend", "role": "developer"}'
```

- **Repositories**:
  - Owners can grant any role; maintainers only up to developer.
  - Groups can be given at most maintainer, and only by someone in the group.
  - A repository always keeps at least one owner. Anyone can leave, except the last owner.
  - Anyone who can read the repository can list its members.
- **Groups**:
  - Any user can create a group (names: lowercase letters, digits, `.`, `_`, `-`) and becomes its owner.
  - The owner and maintainers add and remove members. Only the owner manages maintainers, transfers ownership (`POST /api/v1/group/<name>/transfer`) or deletes the group.
  - Groups are visible to their members only.
- `GET /api/v1/repository` and `GET /api/v1/group` list your repositories and groups.

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
| `registration.require_email_verification` | `REGISTRATION_REQUIRE_EMAIL_VERIFICATION` | `false`; see [Email verification](#email-verification) |
| `http.public_url` | `HTTP_PUBLIC_URL` | none; needed for email links |
| `smtp.host`, `.port`, `.username`, `.password`, `.from`, `.tls` | `SMTP_*` | port `587`, tls `starttls` |
| `storage.type` | `STORAGE_TYPE` | `local` (or `s3`) |
| `storage.local.path` | `STORAGE_LOCAL_PATH` | `./repositories` |
| `storage.s3.bucket`, `.endpoint`, `.accesskey`, `.secretkey`, `.region` | `STORAGE_S3_*` | region `us-east-1` |

`./ogit server --help` lists every flag.

### Email verification

With `registration.require_email_verification: true`, new users receive a link
(`{http.public_url}/api/v1/auth/verify?token=…`, valid
`registration.verification_ttl_hours`) and cannot log in until they open it.
The server refuses to start unless `smtp.host`, `smtp.from` and
`http.public_url` are set.

- Accounts that existed before the upgrade, or were created while the setting was off, count as verified.
- `POST /api/v1/auth/verify/resend {"email": "…"}` sends a new link. It sends at most one per minute, older links stop working, and it gives the same answer whether or not the address is registered.
- If the email can't be sent at registration, the account is still created and the user can resend later.
- `registration.verification_url_template` changes the link, e.g. to point at a web UI that calls `POST /api/v1/auth/verify {"token": "…"}`.

## Not implemented yet

- SSH access
- SSH key management
- Site administrators (the `admin` user role is not used yet)
- Repositories in nested paths (`org/project.git`)

## Development

```bash
go test -race ./...                            # unit and end-to-end tests (fake S3, no Docker)
go test -race -tags=integration -run MinIO ./...  # same suites against MinIO (needs Docker)
go test -race -tags=integration -run Mailpit ./...  # real SMTP delivery through Mailpit (needs Docker)
OGIT_TEST_DB=postgres go test -race -tags=integration ./interfaces/cli/server/  # end-to-end suite on PostgreSQL (needs Docker)
air                                            # live reload, see .air.toml and start.sh
```

Pull requests into `main` and `refactor` run gofmt, `go vet`, the tests and
the MinIO, Mailpit and PostgreSQL integration tests in CI.

## License

See [LICENSE](LICENSE).
