# oGit

A Git server over HTTP with users, access control and pluggable repository
storage (local filesystem or any S3-compatible object store).

## Features

- **Git Smart HTTP**: clone, fetch and push with any git client
- **Users and sessions**: registration and login with JWT sessions, plus a configurable registration policy
- **Two-factor authentication**: authenticator apps (TOTP), security keys and passkeys (WebAuthn, including passwordless sign-in), recovery codes, and re-confirmation before sensitive actions
- **Access control**: public and private repositories, per-repository roles (owner, maintainer, developer, reporter), and personal access tokens with `read:repository` / `write:repository` scopes
- **Storage backends**: local filesystem, or S3-compatible object storage (AWS S3, MinIO, Outscale, ...)
- **Database**: SQLite or PostgreSQL
- **Actions**: GitHub Actions workflows run by `ogit runner` (act, in Docker), with shared, group and repository runners
- **Git LFS**: large files over HTTPS and SSH, stored with the repository (local or S3), file locking, and LFS files served by the web UI, raw links and archives
- **Packages**: a container registry (`docker push` and `pull`), an npm registry, a Go module proxy and generic files, owned by users and groups, linked to repositories and published from Actions
- **Releases**: on tags, with drafts, pre-releases, generated notes, uploaded assets and protected tags; GitHub's releases API for release tools
- **Forks**: fork any repository you can read, open pull requests from your fork, sync it with the original
- **Wikis**: a Markdown wiki per repository, edited in the API or by git push, with history, revert, search and attachments
- **Issues**: per repository, numbered with pull requests, with comments, labels, assignees, milestones and "Fixes #N"
- **Branch protection**: no force-pushes or deletion, pull-request-only branches, per-branch approvals, push and merge allowlists
- **Commit statuses**: CI reports pass or fail per commit (GitHub-compatible API), and protected branches can require checks
- **Notifications**: an inbox of the issues, pull requests and releases that concern you, repository watch levels, and email preferences
- **Webhooks**: GitHub-compatible push, pull request and repository events, signed and retried
- **REST API** with OpenAPI documentation at `/documentation`

The documentation is at **https://svnix-solutions.github.io/ogit/** (sources in [`docs/`](docs/)).

## Try it

```bash
git clone https://github.com/svnix-solutions/ogit.git && cd ogit
docker compose up -d --build     # oGit with PostgreSQL, S3 (MinIO) and Redis
docker compose logs seed         # sign-in details for the demo user
```

Then open `http://localhost:8080`. The
[guide](https://svnix-solutions.github.io/ogit/guides/try-it) explains what's
running and what to try. It is a showcase with public keys, not a production setup.

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

The web UI is at `http://localhost:8080/`, and the API documentation at
`/documentation`. Docker images, production settings, storage, single sign-on,
backups and upgrades are covered in the
[administration docs](https://svnix-solutions.github.io/ogit/admin/).

## Using oGit

The [user docs](https://svnix-solutions.github.io/ogit/user/) cover
[your account](https://svnix-solutions.github.io/ogit/user/account) (two-factor authentication, access tokens, SSH keys),
[git over HTTP and SSH](https://svnix-solutions.github.io/ogit/user/git),
[repositories](https://svnix-solutions.github.io/ogit/user/repositories), [groups](https://svnix-solutions.github.io/ogit/user/groups),
[pull requests](https://svnix-solutions.github.io/ogit/user/pull-requests), [branch protection](https://svnix-solutions.github.io/ogit/user/branch-protection),
[issues](https://svnix-solutions.github.io/ogit/user/issues), [wikis](https://svnix-solutions.github.io/ogit/user/wikis), [notifications](https://svnix-solutions.github.io/ogit/user/notifications),
[forks](https://svnix-solutions.github.io/ogit/user/forks), [mirroring and imports](https://svnix-solutions.github.io/ogit/user/mirroring), [Git LFS](https://svnix-solutions.github.io/ogit/user/lfs),
[Actions](https://svnix-solutions.github.io/ogit/user/actions), [releases](https://svnix-solutions.github.io/ogit/user/releases) and [packages](https://svnix-solutions.github.io/ogit/user/packages).
Guides walk through [migrating from GitHub](https://svnix-solutions.github.io/ogit/guides/migrate-from-github),
[running Actions](https://svnix-solutions.github.io/ogit/guides/run-actions) and [publishing a container image](https://svnix-solutions.github.io/ogit/guides/publish-a-container).

## Integrations

### Repository tokens (CI)

Maintainers and owners create tokens bound to one repository, in its
**Settings** page or through the API, so CI needs no personal account:

```bash
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/tokens -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "ci", "scopes": ["read:repository", "write:status"]}'
git clone https://ci:$TOKEN@git.example.com/me/my-project.git
curl localhost:8080/api/v1/repository/me%2Fmy-project/issues -H "Authorization: token $TOKEN"
```

- **Scopes:**
  - `read:repository`: clone, fetch, and read this repository's API (files, pull requests, issues, milestones, statuses);
  - `write:repository`: push, under branch protection (a token is never on a rule's list);
  - `write:status`: post commit statuses.
- **Limits:** a token reaches its own repository only. It never acts as a person, so no comments, reviews or other writes through the API. Expiry is optional, the value is shown once at creation, and the token list shows each token's last use.

### Commit statuses (CI)

CI reports results on commits like it does to GitHub. Create a personal access
token with the `write:status` scope: it can post statuses but can't push or
clone (`write:repository` tokens can post too).

```bash
# oGit's API
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/statuses/<sha> -H "Authorization: token $CI_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"state": "success", "context": "ci/build", "description": "All tests passed", "target_url": "https://ci.example.com/runs/42"}'
curl "localhost:8080/api/v1/repository/me%2Fmy-project/status?ref=main" -H "Authorization: token $CI_TOKEN"   # combined
# GitHub's API paths, for CI plugins that report to GitHub (Enterprise): API URL http://<server>/api/v3
curl -X POST localhost:8080/api/v3/repos/me/my-project/statuses/<sha> -H "Authorization: token $CI_TOKEN" -d '{"state": "pending", "context": "ci/build"}'
curl localhost:8080/api/v3/repos/me/my-project/commits/main/status -H "Authorization: token $CI_TOKEN"
```

- **States:** `pending`, `success`, `failure` or `error`. Each `context` (default `default`) counts with its latest report. The combined state is `failure` if any context failed or errored, else `pending` if any is pending (or none was reported), else `success`.
- **Who:** developers and up post; anyone who can read the repository reads. Tokens are accepted as `token <pat>`, `Bearer <pat>` or Basic with the token as the password. Bad tokens count like failed logins for the IP.
- **Required checks:** a branch protection rule's `required_contexts` must each be `success` on a pull request's head commit before it can be merged (a new push needs new reports). The pull request's `required_checks` lists their states.
- **Webhooks:** each status is a GitHub `status` event.
- `target_url` must be `http` or `https`; descriptions are cut to 140 characters. Only the status endpoints exist under `/api/v3`.

### Webhooks

Maintainers and owners add webhooks to a repository, in its **Settings** page
(with the delivery log, payloads and redelivery) or through the API. Payloads and headers follow
GitHub's (`X-GitHub-Event`, `X-GitHub-Delivery`, `X-Hub-Signature-256`), so
existing receivers and CI integrations work.

```bash
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/hooks -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"url": "https://ci.example.com/hook", "secret": "s3cret", "events": ["push", "repository"]}'
# Delivery log (newest 100), one delivery with its payload and response, redeliver, ping
curl localhost:8080/api/v1/repository/me%2Fmy-project/hooks/<hook-id>/deliveries -H "Authorization: Bearer $SESSION"
curl localhost:8080/api/v1/repository/me%2Fmy-project/hooks/<hook-id>/deliveries/<id> -H "Authorization: Bearer $SESSION"
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/hooks/<hook-id>/deliveries/<id>/attempts -H "Authorization: Bearer $SESSION"
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/hooks/<hook-id>/pings -H "Authorization: Bearer $SESSION"
```

- **Events:** `gollum` (wiki pages created or edited, see [Wikis](https://svnix-solutions.github.io/ogit/user/wikis)), `status` (commit statuses), `push` (the default; HTTP, SSH and pull request merges, including deleted branches), `repository` (renamed, transferred, publicized, privatized, archived, unarchived, edited), or `*` for all. A `ping` is sent when a hook is created.
  - `pull_request`: opened, edited, closed (with `merged: true` for merges), reopened, synchronize (a push to the source branch), converted_to_draft, ready_for_review, labeled, unlabeled.
  - `pull_request_review`: submitted. `pull_request_review_comment`: inline comments created, edited, deleted (also those of a review).
  - `issue_comment`: conversation comments created, edited, deleted; for pull requests the `issue` has a `pull_request` field, as on GitHub.
  - `issues`: opened, edited, closed, reopened, labeled, unlabeled, assigned, unassigned, milestoned, demilestoned, deleted. `issue_comment` for issue comments has no `pull_request` field. `milestone`: created, edited, closed, opened, deleted. Pull requests also send assigned, unassigned, milestoned and demilestoned.
  - `release`: created, published, released, prereleased, edited, deleted, with the assets; a tag created by a release is also a `push`. Drafts send nothing until published.
  - `fork`: sent to the original's hooks.
  - Deliveries are sent in parallel, so their order is not guaranteed.
- **Signing:** with a secret, `X-Hub-Signature-256` is `sha256=` plus the HMAC-SHA256 of the body. Secrets are write-only.
- **Delivery:** events are queued in the database and sent in the background by every server, each delivery by one of them. A 2xx answer is a success; anything else is retried after 30s, 2m, 10m and 30m, then marked failed. Redirects are not followed.
- **Private networks:** targets on private, loopback or link-local addresses are refused unless listed in `webhooks.allowed_hosts`.
- **URLs** in payloads are built from `http.public_url`; set it.

### GitHub-compatible releases API (goreleaser and the like)

`/api/v3/repos/<owner>/<repo>/releases` answers GitHub's releases endpoints: list, get, by tag, latest, create (with `generate_release_notes`), edit, delete, generate notes, and assets (list, get, download with `Accept: application/octet-stream`, edit, delete). Uploads go to the `upload_url` of a release, under `/api/uploads` (GitHub Enterprise's upload URL). Ids are numbers there, since GitHub's clients parse them as integers. Authenticate with a personal access token or a repository token. For goreleaser:

```yaml
github_urls:
  api: https://git.example.com/api/v3/
  upload: https://git.example.com/api/uploads/
  download: https://git.example.com/
```

### Sessions in the web UI

The UI signs in with `POST /api/v1/auth/session`, which keeps the session in
an HttpOnly cookie (`ogit_session`) that page scripts can't read. Writes
authenticated by that cookie must also send the `ogit_csrf` cookie's value in
an `X-CSRF-Token` header. API clients keep using bearer tokens, which need
neither. Set `http.public_url` to an `https://` URL behind a TLS proxy so the
cookies get the `Secure` flag (it is also set when the request arrives over
HTTPS or with `X-Forwarded-Proto: https`).

## Running a server

The [administration docs](https://svnix-solutions.github.io/ogit/admin/) cover
[installation](https://svnix-solutions.github.io/ogit/admin/install) (binary and Docker, the first admin),
[configuration](https://svnix-solutions.github.io/ogit/admin/configuration),
[database](https://svnix-solutions.github.io/ogit/admin/database) and [storage](https://svnix-solutions.github.io/ogit/admin/storage) (local and S3, `ogit storage gc` and `prune`),
[reverse proxies](https://svnix-solutions.github.io/ogit/admin/reverse-proxy),
[registration and email](https://svnix-solutions.github.io/ogit/admin/registration),
[two-factor authentication](https://svnix-solutions.github.io/ogit/admin/two-factor),
[single sign-on](https://svnix-solutions.github.io/ogit/admin/sso) (OIDC, GitHub, LDAP, SAML),
[rate limits](https://svnix-solutions.github.io/ogit/admin/rate-limits),
[site administration](https://svnix-solutions.github.io/ogit/admin/administration),
[upgrades](https://svnix-solutions.github.io/ogit/admin/upgrades), [backups](https://svnix-solutions.github.io/ogit/admin/backups) and
[monitoring](https://svnix-solutions.github.io/ogit/admin/monitoring). The [guides](https://svnix-solutions.github.io/ogit/guides/) walk through
common setups.

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

Pull requests into `main` run gofmt, `go vet`, the tests and
the MinIO, Mailpit, Redis and PostgreSQL integration tests in CI.

## License

See [LICENSE](LICENSE).
