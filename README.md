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

Building needs Go (see `go.mod`) and a C toolchain, because the SQLite driver
uses CGO, and Node.js (see `web/.nvmrc`) for the web UI.

```bash
npm --prefix web ci && npm --prefix web run build   # the web UI, embedded in the binary
go build -o ogit ./cmd/cmd.go

cp config-example.yaml config.yaml   # then edit it
./ogit migration -c config.yaml      # create or upgrade the database schema
./ogit server -c config.yaml         # listens on :8080 by default
```

The web UI is at `http://localhost:8080/`. A binary built without the UI
step still works and shows how to build it there.

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

Repositories live under their owner, a user or a group:
`<owner>/<slug>` (`My Project` created by `me` → `me/my-project`). Git asks
for credentials: the username is the **token name** and the password is the
**token**.

```bash
git clone http://localhost:8080/me/my-project.git
```

In the API, a repository is identified by its id or by its path with the
slash URL-encoded: `/api/v1/repository/me%2Fmy-project`.

| Caller | Clone / fetch | Push |
|---|---|---|
| Anonymous | public repositories only | never |
| Token with `read:repository` | public repos, and private repos the user can read | no |
| Token with `write:repository` | as above | repos where the user is developer or higher |

Private repositories that the caller can't read are reported as not found.

### Pull requests

Developers propose merging one branch into another of the same
repository; maintainers and owners merge them.

```bash
R=localhost:8080/api/v1/repository/me%2Fmy-project
curl -X POST $R/pulls -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"title": "Add login", "source": "feature/login", "draft": false}'      # into the default branch
curl "$R/pulls?state=open"                                                   # also merged, closed, all
curl "$R/pulls/1"                                                            # mergeable? conflicting files
curl -X POST $R/pulls/1/merge -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"method": "squash", "delete_branch": true}'                          # or merge, rebase
```

The server merges without a git binary, file by file: if both branches
changed the same file since they diverged, the pull request shows the
conflicting files and the author merges the target into their branch
locally. The target branch only moves if nobody pushed to it during the
merge. Drafts can't be merged until marked ready (`PATCH {"draft": false}`).
Anyone who can read the repository can read its pull requests.

Reviews and conversation:

```bash
curl -X POST $R/pulls/1/comments -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"body": "Can we rename this? cc @alice", "inline": {"path": "src/a.go", "line": 12, "side": "new"}}'
curl -X POST $R/pulls/1/reviews -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"state": "approved", "body": "LGTM"}'                                 # or changes_requested, commented
curl -X PATCH localhost:8080/api/v1/repository/me%2Fmy-project -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"required_approvals": 1}'        # owners
```

- Anyone who can read the repository comments and reviews. Approvals and change requests count from developers and up (each reviewer's latest), never the author's own.
- Merging needs `required_approvals` approvals (0 by default) and no outstanding request for changes.
- Maintainers manage labels (`/labels`); developers set them on pull requests (`PUT /pulls/1/labels`); `?label=` filters the list.
- With SMTP configured, the author, reviewers, commenters and anyone @mentioned (who can read the repository) are emailed about others' comments, reviews, merges and closes. Set `http.public_url` for links.

### Raw files and archives

```bash
curl -O http://localhost:8080/me/my-project/raw/main/docs/guide.md           # a file at a branch, tag or commit
curl -O http://localhost:8080/me/my-project/archive/v1.0.tar.gz              # or .zip
curl -u laptop:$TOKEN -O http://localhost:8080/me/private-project/raw/main/x # private: an access token, like git
```

Public repositories need no credentials. Branch names may contain slashes
(`raw/feature/x/README.md`). Files are served as plain text or as
downloads, never as web pages, so a file in a repository can't run scripts
on the server's site.

The read API behind the web UI (`/api/v1/repository/<id>/refs`, `tree`,
`blob`, `commits`, `commits/<sha>`, `compare`, `blame`) also works without a
session for public repositories; `/documentation` describes it.

### Git over SSH

Enable it with `ssh.enabled: true`. It listens on `ssh.port` (default 2222).
Everyone connects as `git`, and the key identifies the account:

```bash
# Add your public key (ed25519, ECDSA, or RSA with at least 3072 bits)
curl -X POST localhost:8080/api/v1/user/ssh-keys -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d "{\"key\": \"$(cat ~/.ssh/id_ed25519.pub)\"}"

git clone ssh://git@localhost:2222/me/my-project.git
ssh -T -p 2222 git@localhost      # "Hi <username>! ..."
```

- **Same access rules as HTTP:** private repositories return "not found" to non-members, reporters can't push, and disabled or unverified accounts are rejected.
- **Deploy keys** belong to one repository and are read-only unless `"read_only": false`. Its maintainers and owners manage them at `/api/v1/repository/<owner>%2F<slug>/deploy-keys`; use them for CI.
- **One place per key:** a key can be used once, on one account or as one deploy key. When SMTP is configured, users are emailed when a key is added to their account.
- **Admins** can list and revoke a user's keys at `/api/v1/admin/users/<username>/ssh-keys`.
- **Host key:** an ed25519 key is generated at `ssh.host_key_path` on first start. Keep that file; in Docker, put it on a volume and publish the port (`-p 2222:2222`). If it changes, clients warn about a changed host key.

### Managing a repository

```bash
# Owners: rename (the old URL keeps working), change visibility, archive (read-only)
curl -X PATCH localhost:8080/api/v1/repository/me%2Fmy-project -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "Better Name", "is_private": false, "archived": true}'
# Maintainers and owners: description
curl -X PATCH localhost:8080/api/v1/repository/me%2Fbetter-name -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"description": "What it does"}'
# Owners: move it to a group you maintain or own (or back to your namespace)
curl -X POST localhost:8080/api/v1/repository/me%2Fbetter-name/transfer -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"namespace": "backend"}'
# Owners: delete permanently (repeat the slug)
curl -X DELETE "localhost:8080/api/v1/repository/backend%2Fbetter-name?confirm=better-name" -H "Authorization: Bearer $SESSION"
# Browse public repositories
curl "localhost:8080/api/v1/repository?scope=public&q=tools" -H "Authorization: Bearer $SESSION"
```

- **Renaming and moving:** the path changes, and the old one keeps working for the API, HTTP and SSH until another repository takes it. Git data never moves.
- **Archiving:** an archived repository can be cloned and fetched, but refuses pushes from everyone until it's unarchived.

### Namespaces, members and groups

Every repository belongs to a user or a group, its namespace. Usernames and
group names are one set of names (lowercase letters, digits, `.`, `_`, `-`;
not ending in `.git`; a few words such as `admin`, `api`, `explore`,
`settings` are reserved), so `/<name>` is never ambiguous.

- **Your namespace:** you create repositories there by default and own them.
- **A group's namespace:** its owner and maintainers create repositories there with `{"namespace": "<group>", "name": ...}`. Group members get their group role on all of the group's repositories: the owner is owner, maintainers are maintainers, members are developers.
- A user or group that still has repositories in its namespace can't be deleted; move or delete them first.

Repositories created before namespaces existed moved under their first owner
(`/my-project.git` became `/<owner>/my-project.git`); the old URLs no longer
work.

The creator of a repository in their own namespace is its owner. Owners and
maintainers share it with other users or groups (`/documentation` lists every
endpoint):

```bash
# Give a user access: reporter (read), developer (push), maintainer, owner
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "alice", "role": "developer"}'

# Create a group, add people, then give the whole group access
curl -X POST localhost:8080/api/v1/group -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "backend"}'
curl -X POST localhost:8080/api/v1/group/backend/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "bob", "role": "member"}'
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"group": "backend", "role": "developer"}'
```

- **Repositories**:
  - Owners can grant any role; maintainers only up to developer.
  - Groups can be given at most maintainer, and only by someone in the group.
  - A repository always keeps at least one owner. Anyone can leave, except the last owner.
  - Anyone who can read the repository can list its members.
- **Groups**:
  - Any user can create a group (its name follows the namespace rules above) and becomes its owner.
  - The owner and maintainers add and remove members. Only the owner manages maintainers, transfers ownership (`POST /api/v1/group/<name>/transfer`) or deletes the group.
  - Groups are visible to their members only.
- `GET /api/v1/repository` and `GET /api/v1/group` list your repositories and groups.

## Administration

Make the first site admin from the server. The command uses the database
directly, like `migration`:

```bash
./ogit admin promote -c config.yaml --email you@example.com   # or --username
./ogit admin demote  -c config.yaml --username someone
# Docker: docker exec <container> ./app admin promote -c /config/config.yaml --email you@example.com
```

Admins use `/api/v1/admin` (`/documentation` lists the details):

| Endpoint | |
|---|---|
| `GET /api/v1/admin/users?q=&limit=&offset=` | list and search users |
| `POST /api/v1/admin/users` | create a verified account with an initial password (works when registration is off) |
| `PATCH /api/v1/admin/users/<username>` | `{"active": false}` disables the account and ends its sessions immediately; also `"email_verified": true`, `"role": "admin"` or `"user"` |
| `DELETE /api/v1/admin/users/<username>` | delete a user and their sessions, tokens and memberships |
| `GET /api/v1/admin/repositories` | list every repository |
| `POST /api/v1/admin/repositories/<owner>%2F<slug>/owner` | make a user owner, e.g. when a repo's owners are gone |
| `POST /api/v1/admin/repositories/<owner>%2F<slug>/transfer` | `{"namespace": "..."}` moves it to any user's or group's namespace |
| `DELETE /api/v1/admin/repositories/<owner>%2F<slug>?confirm=<slug>` | permanently delete a repository and its git data |
| `POST /api/v1/admin/groups/<name>/transfer` | make a user the group owner |

- Admins have no implicit access to repository content. To get access, they make themselves owner, which shows in the member list.
- A user is not deleted while they're the only owner of a repository, have repositories in their namespace, or own a group. The error lists them, so the admin can hand them over (or move them) first.
- The API never removes the last active admin; the CLI can always promote one.
- Admin actions are logged with `"component":"audit"`.

Users change their own password with `POST /api/v1/user/password`
(`current_password`, `new_password`), which also ends their other sessions.

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
| `ssh.enabled` / `ssh.port` / `ssh.host_key_path` | `SSH_ENABLED` / `SSH_PORT` / `SSH_HOST_KEY_PATH` | `false` / `2222` / `./ssh_host_ed25519_key` |
| `storage.type` | `STORAGE_TYPE` | `local` (or `s3`) |
| `storage.local.path` | `STORAGE_LOCAL_PATH` | `./repositories` |
| `storage.s3.bucket`, `.endpoint`, `.accesskey`, `.secretkey`, `.region` | `STORAGE_S3_*` | region `us-east-1` |
| `storage.s3.cache.type` / `.max_mb` / `.redis_url` | `STORAGE_S3_CACHE_*` | `memory` / `256` / none; see [S3 storage](#s3-storage) |

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

### S3 storage

Repositories on S3 are stored as git packs (`objects/pack/pack-<sha>.pack` and
`.idx`), one per push, read with ranged requests. Several servers can share
one bucket: ref updates use conditional writes (`If-Match` /
`If-None-Match`), so concurrent pushes to the same branch can't overwrite
each other, and each request lists the packs again, so it sees the latest
push.

Packs never change once written, so their indexes and 4 MiB blocks are
cached by `storage.s3.cache.type`:

- `memory` (default): an LRU in each server, limited to `max_mb`.
- `redis`: one cache shared by every server (`redis_url`). If Redis is down, requests go to S3 and Redis is retried after 30 seconds.
- `none`: every read goes to S3.

On a 300-commit repository, the number of S3 requests went from 1810 to 12
for a push, from 1513 to 12 for a clone and from 1256 to 14 for a one-commit
fetch.

Each push adds a pack, and each request lists and loads every pack, so a
repository gets slower with every push (a clone after 40 pushes takes 92
requests instead of 12). Merge them regularly, e.g. from a nightly cron job:

```bash
./ogit storage gc -c config.yaml --all             # or --repository <owner>/<name>
```

It merges the small packs and leaves the big old ones alone, so each run
costs about the size of the recent pushes. Objects are only moved, never
pruned, so it cannot lose data, and it is safe while the server is running
and pushes are arriving. The packs it replaces are hidden at once, but kept
for `--grace` (default 1h) for requests that were already reading them, and
deleted by the next run after that. It also packs loose objects (see below).

`gc` never deletes objects. Commits dropped by force-pushes or deleted
branches stay until you prune them, e.g. weekly:

```bash
./ogit storage prune -c config.yaml --all --dry-run   # report only
./ogit storage prune -c config.yaml --all             # --expire 336h (2 weeks) by default
```

An object is pruned only when no ref (branches, tags, notes, any `refs/`
name, HEAD) reaches it and it is older than `--expire`, so force-pushed work
can be recovered by its hash for two weeks. Objects needed by recent packs,
such as a push still in progress, are kept. Objects that `gc` has just
merged sit in a new pack, so their two weeks start again. Prune rewrites the repository's
old packs, so it costs a full read and write of them. The same `--grace`
rule as `gc` applies to the packs it replaces. If an object is missing from
the repository, prune stops without deleting anything.

Repositories created before packs (one S3 object per git object) still
work, but stay slow until they are converted:

```bash
./ogit storage repack -c config.yaml --repository <owner>/<name>   # or --all
```

The command writes one pack, checks that every object is in it, then deletes
the loose objects. The server can keep running, but a clone or fetch of that
repository that is in progress when the loose objects are deleted can fail
and must be retried, so run it when the repository is quiet.

### Web UI

The web UI covers what the API does:

- **Repositories:** `/explore` (public, no account needed), `/<owner>` (a user's or group's repositories), `/<owner>/<repo>` (clone URLs for HTTPS and SSH), and `/<owner>/<repo>/settings` (rename, visibility, archive, members, deploy keys, transfer, delete). `/new` creates one in your namespace or a group you maintain.
- **Groups:** `/groups` and `/groups/<name>` (members, roles, ownership, deletion).
- **Your account:** `/settings` (password, access tokens, SSH keys).
- **Admins:** `/admin` (users, every repository, group ownership).

- **Code:** `/<owner>/<repo>` shows the files and README of the default branch (signed out too, for public repositories), with `tree/`, `blob/` (syntax highlighting, rendered Markdown, raw), `blame/`, `commits/`, `commit/<sha>`, `branches` (with zip and tar.gz downloads) and `compare/<base>...<head>`.

- **Pull requests:** `/<owner>/<repo>/pulls` (open, merged, closed, label filter), `pulls/new` (pick branches, preview the diff), and each pull request with its conversation (Markdown with @mentions and #123 links, reviews, merge box listing what blocks the merge), commits and files changed (comment on any line). Repository settings hold labels and the approvals required to merge.

README images are shown only from the repository itself: the page's security policy blocks images from other sites (e.g. badges).

The UI signs in with `POST /api/v1/auth/session`, which keeps the session in
an HttpOnly cookie (`ogit_session`) that page scripts can't read. Writes
authenticated by that cookie must also send the `ogit_csrf` cookie's value in
an `X-CSRF-Token` header. API clients keep using bearer tokens, which need
neither. Set `http.public_url` to an `https://` URL behind a TLS proxy so the
cookies get the `Secure` flag (it is also set when the request arrives over
HTTPS or with `X-Forwarded-Proto: https`).

## Not implemented yet

- SSH key management
- Sub-groups (`org/team/project.git`)

## Development

```bash
go test -race ./...                            # unit and end-to-end tests (fake S3, no Docker)
go test -race -tags=integration -run MinIO ./...  # same suites against MinIO (needs Docker)
go test -race -tags=integration -run Mailpit ./...  # real SMTP delivery through Mailpit (needs Docker)
go test -race -tags=integration -run Redis ./...    # shared Redis cache (needs Docker)
OGIT_TEST_DB=postgres go test -race -tags=integration ./interfaces/cli/server/  # end-to-end suite on PostgreSQL (needs Docker)
air                                            # live reload, see .air.toml and start.sh
npm --prefix web run dev                       # UI on :5173 with hot reload; proxies /api to :8080
npm --prefix web test                          # UI unit tests (vitest)
```

Pull requests into `main` and `refactor` run gofmt, `go vet`, the tests and
the MinIO, Mailpit, Redis and PostgreSQL integration tests in CI.

## License

See [LICENSE](LICENSE).
