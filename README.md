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

The [integration docs](https://svnix-solutions.github.io/ogit/integrate/) cover the [REST API](https://svnix-solutions.github.io/ogit/integrate/)
(OpenAPI at `/documentation`), [tokens](https://svnix-solutions.github.io/ogit/integrate/tokens) for CI,
[webhooks](https://svnix-solutions.github.io/ogit/integrate/webhooks), [commit statuses](https://svnix-solutions.github.io/ogit/integrate/statuses)
and the [GitHub-compatible API](https://svnix-solutions.github.io/ogit/integrate/github-api) (goreleaser and CI plugins).

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

## Contributing

[How oGit is built](https://svnix-solutions.github.io/ogit/contribute/) and [development](https://svnix-solutions.github.io/ogit/contribute/development)
(building, tests, conventions, sending a change).

## License

See [LICENSE](LICENSE).
