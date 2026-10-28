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
- The author and developers and up request reviews (`PUT /pulls/1/reviewers {"reviewers": ["alice"]}`) from anyone who can read the repository; a request goes away when that person reviews. Webhooks get `review_requested` / `review_request_removed`.
- Maintainers manage labels (`/labels`); developers set them on pull requests (`PUT /pulls/1/labels`); `?label=` filters the list.
- The author, requested reviewers, reviewers, commenters and anyone @mentioned (who can read the repository) are notified about others' comments, reviews, merges and closes (see Notifications).

### Notifications

Each issue, pull request or release that concerns you is one entry in your inbox (`GET /api/v1/notifications`, unread by default; `?all=true`, `?participating=true`, `?repository=`), made unread again by each new event on it.

- **Participating** (always): you're mentioned, assigned, asked for a review, the author, a reviewer or commenter, or a check fails on your pull request.
- **Watching** (`PUT /api/v1/repository/<owner>%2F<repo>/watch {"level": …}`): `participating` (the default), `all` (also new issues, pull requests and releases), `releases`, or `ignore` (nothing, not even mentions). You watch repositories you create.
- `PATCH /api/v1/notifications/<id> {"unread": false}`, `POST /api/v1/notifications/read` (everything, or `{"repository": …}`), `POST /api/v1/notifications/<id>/mute` to unsubscribe from a thread (`DELETE` to subscribe again), `GET /api/v1/notifications/count`.
- **Email** (with SMTP; `PUT /api/v1/notifications/settings {"email": …}`): `participating` (the default), `all` (also watched activity) or `none`. Set `http.public_url` for links.
- Nobody is notified about their own actions, or about a repository they can't read.
- In the web UI: the bell in the header (unread count), the inbox at `/notifications`, the Watch menu on each repository, and the email choice in Settings.

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

### Push size

Pushes over HTTP and SSH are streamed (never held in memory) and refused above `git.max_push_size` (2 GB by default, `0` for no limit); git shows `push is larger than the limit`. A refused or interrupted push leaves nothing behind in storage.

### Actions

Workflows use GitHub Actions syntax, in `.ogit/workflows/` (or `.github/workflows/` when that directory has none), and run on your own runners with [act](https://github.com/nektos/act), in Docker.

```sh
# A registration token: repository maintainers, group owners or site admins
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/actions/runners/registration-token -H "Authorization: Bearer $SESSION"
#   (group: POST /api/v1/group/<name>/runners/registration-token; shared: POST /api/v1/admin/runners/registration-token)

# On a machine with Docker
ogit runner register --url https://git.example.com --token "$REGISTRATION_TOKEN" --labels ubuntu-latest:docker://node:20-bookworm
ogit runner run
```

- **Triggers:** `push` (`branches`, `tags`, `paths` and their `-ignore` filters), `pull_request` (`types`, base `branches`, `paths`), `workflow_dispatch` (writers: `POST .../actions/workflows/<file>/dispatches {"ref", "inputs"}`), `schedule` (default branch, at most every 5 minutes), `issues`, `issue_comment`, `release`, `create` and `delete` (the default branch's workflows; `types:` filters the action).
- **Pull requests** run on `refs/pull/<n>/merge`, the head merged into the base (the head when they conflict), and post their statuses on the head. Runs from forks get no secrets and a read-only token; a first-time contributor's (no merged pull request here) wait until a maintainer approves them: `POST .../actions/runs/<n>/approve`.
- **Secrets and variables** per repository (`/api/v1/repository/<owner>%2F<repo>/actions/secrets/<NAME>`, maintainers), group (`/api/v1/group/<name>/actions/secrets/<NAME>`, its owner) and site (`/api/v1/admin/actions/secrets/<NAME>`, admins); `PUT {"value"}`, `DELETE`, and `.../variables/...` likewise. Secrets need `security.encryption_key`, are stored encrypted and never read back; the repository's override the group's, which override the site's.
- **Commit statuses:** each job posts `<workflow> / <job> (<event>)`, which branch protection can require.
- **Runners** take queued runs from their scope (one repository, a group's repositories, or every repository for shared runners) when they have every `runs-on` label of the workflow. A run is taken by one runner, which runs its jobs (`needs`, matrices) with act. Labels map to Docker images (`name:docker://image`); the defaults map `ubuntu-latest`, `ubuntu-24.04` and `ubuntu-22.04` to Node images with git.
- **`GITHUB_TOKEN`** is a job token valid while the run runs, for its repository only: it reads it (`actions/checkout`, over git or GitHub's archive API `/api/v3/repos/<owner>/<repo>/tarball/<ref>`), pushes with `permissions: contents: write` (pushes by workflows start no workflows) and posts statuses with `statuses: write`. Without `permissions:` it only reads; from forks, always. `GITHUB_SERVER_URL` is `http.public_url`, which job containers must reach (`ogit runner run --network` picks their Docker network).
- **Actions** (`uses:`) are fetched from this server first, then from github.com.
- **Runs:** `GET /api/v1/repository/<owner>%2F<repo>/actions/runs` (`?workflow=&branch=&event=&status=&pull=`), `.../runs/<n>` (with jobs), `.../runs/<n>/logs?from=` (incremental), `POST .../runs/<n>/cancel` and `.../rerun` (writers). A run whose runner stops reporting for 10 minutes fails.
- **Web UI:** an Actions tab (once the repository has workflows or runs) lists runs by workflow, branch, event and status; a run's page shows its jobs and follows its log live, with cancel, re-run and "Approve and run" for waiting fork runs. Workflows with `workflow_dispatch` have a Run workflow form for their inputs. Pull requests list their runs next to their checks. Runners, secrets and variables are in the repository settings, the group page (its owner) and the admin page.
- **Artifacts:** use `ogit/upload-artifact@v4` and `ogit/download-artifact@v4` (same inputs and outputs as `actions/upload-artifact@v4` and `download-artifact@v4`, which refuse servers other than github.com). The runner carries them, so nothing is downloaded. Artifacts are stored with the repository, up to `actions.max_artifact_size` (2 GB), kept `actions.artifact_retention_days` (90; `retention-days` can only shorten it), listed and downloaded from the run page by anyone who can read the repository, and deleted by writers. `download-artifact` with `run-id` and `github-token` reads another run's, through GitHub's REST API (`/api/v3/repos/<owner>/<repo>/actions/runs/<id>/artifacts`, `.../artifacts/<id>/zip`).
- **Cache:** `actions/cache@v4` works as is: on servers other than github.com it uses GitHub's v1 cache API, which oGit serves (`ACTIONS_CACHE_URL`, set by the runner). A run restores entries of its ref, then its pull request's base branch, then the default branch (exact key, then `restore-keys` prefixes, same version), and saves to its own ref only (never from forks; a key is saved once per ref). Each repository keeps `actions.cache_size_limit` (10 GB): the least recently used entries go first, and entries unused for 7 days are deleted. Maintainers see the entries and their use of the limit in the repository settings, and delete them. GNU tar must be in the job's image (the default Node images have it; Alpine's BusyBox tar doesn't).
- **Badges:** `/<owner>/<repo>/actions/workflows/<file>/badge.svg[?branch=]` shows the latest finished run of a workflow on a branch (the default one if omitted).

### Git LFS

Git LFS works with the usual remote, over HTTP or SSH: `git lfs track "*.psd"`, commit and push.

- **Auth:** git's. Access tokens (HTTP Basic, as for git), repository tokens and anonymous reads of public repositories; over SSH (also deploy keys), `git-lfs-authenticate` returns a token valid for an hour. SSH needs `http.public_url`, where the client sends the transfers.
- **Storage:** objects are stored next to the repository's git data (local or S3), streamed, up to `lfs.max_object_size` (2 GB by default). An upload whose content doesn't match its oid and size is refused and not kept.
- **Forks** read the objects they lack from the repository they were forked from (if you can read it); objects pushed to a fork are stored in the fork.
- **Locks:** `git lfs lock <file>`, `git lfs locks`, `git lfs unlock <file>`. Writers lock; only the lock's owner may push changes to that file (the server refuses others' pushes, whatever the client does). Owners unlock theirs; maintainers and owners force-unlock (`--force`).
- **Web UI, raw files and archives:** an LFS file shows "Stored with Git LFS" with its size; raw links and archives serve its content (or the pointer, if the object isn't on the server).

### Packages

Packages belong to a user or group, like repositories, and may be linked to one of its repositories. Publish with a personal access token with `write:packages` (`read:packages` to pull private ones), or from Actions with `permissions: packages: write`.

- **Container images:** `docker login <host>` with your username and the token as the password (in Actions: `echo "$GITHUB_TOKEN" | docker login <host> -u x --password-stdin`), then `docker push <host>/<owner>/<image>:<tag>` and `docker pull`. The registry serves the OCI distribution API under `/v2` on the site's host (HTTPS: docker only talks to plain HTTP for `localhost`, 127.0.0.0/8 or registries listed as `insecure-registries`). Layers are shared between the images of a namespace.
- **npm:** packages are scoped by their owner, `@<owner>/<name>`. In `.npmrc`: `@<owner>:registry=<url>/api/packages/npm/` and `//<host>/api/packages/npm/:_authToken=<token>` (in Actions, `NODE_AUTH_TOKEN` with `setup-node`'s `registry-url`). `npm publish`, `install`, `view`, `dist-tag`, `deprecate`, `unpublish`, `search` and `whoami` work; `npm login` doesn't (use a token). Versions are immutable once published, and their integrity is checked. Writers deprecate and manage dist-tags; package admins unpublish (`latest` then follows the highest version left). The README shown is the one in the tarball.
- **Go modules:** every repository is a module, `<host>/<owner>/<repo>` (`/vN` for major versions 2 and up), served by a module proxy from its tags (semver tags whose go.mod names the module; v0 and v1 tags without a go.mod work too); branches and commits resolve to pseudo-versions, and nothing is published. `go env -w GOPRIVATE=<host>` and `go get <host>/<owner>/<repo>`: `?go-get=1` pages point the go command at the proxy (`<url>/api/packages/go`, which also works as `GOPROXY`). For private repositories, add `machine <host> login <user> password <token>` to `~/.netrc` (a token that reads the repository; go sends it over HTTPS only). `<host>` is `http.public_url`'s host name: module paths can't have ports, and the go command needs a dot in it (`git.example.com`, not `localhost`). Sub-directory modules aren't served.
- **Generic files:** `curl -u <user>:<token> --upload-file tool.tar.gz <url>/api/packages/<owner>/generic/<name>/<version>/tool.tar.gz`, downloaded at the same URL. A version's file is published once; downloads are always attachments.
- **Who publishes:** the namespace's user, a group's maintainers and owners, the linked repository's developers and up, and that repository's tokens (Actions jobs included, which create packages linked to their repository).
- **Visibility:** private by default; linked packages may follow their repository (public with it), or be made public. Readers of the linked repository read the package.
- **Limits:** files and layers up to `packages.max_file_size` (2 GB).
- **Deleting:** package admins (the namespace's user, group maintainers and owners, the linked repository's maintainers and owners) delete versions (for images: a digest; deleting a tag only untags) and packages. Layers no image uses any more are deleted after a day. Deleting a repository unlinks its packages and makes them private; users and groups with packages can't be deleted.
- **Web UI:** a namespace's Packages tab lists its packages by type; repositories with linked packages or a Go module get a Packages tab (the module's versions, requirements and `go get` command); a package's page shows its install setup, versions (tags, files, deprecations), npm README and dependencies, and settings.
- **API:** `GET /api/v1/packages/<owner>[?type=]`, `GET|PATCH|DELETE /api/v1/packages/<owner>/<type>/<name>` (image names with slashes URL-encoded; `DELETE` takes `?confirm=<name>`), `DELETE .../versions/<version>`, `GET /api/v1/repository/<owner>%2F<repo>/packages` and `.../go-module[?summary=true]`.

### Releases

```bash
# On an existing tag, or a new one created from target_commitish when published
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/releases -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"tag_name": "v1.2.0", "target_commitish": "main", "name": "v1.2.0", "body": "..."}'
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/releases/generate-notes -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"tag_name": "v1.2.0"}'
curl localhost:8080/api/v1/repository/me%2Fmy-project/releases/latest
```

- **Who:** writers (developers and up, or a repository token with `write:repository`, for CI) create, edit, publish and delete releases. Anyone who can read the repository sees published ones.
- **Tags:** a release uses an existing tag, or creates one from `target_commitish` (a branch or commit; the default branch when empty) when it is published. Deleting a release keeps its tag.
- **Drafts:** visible to writers only. They create no tag and send no webhooks until published (`"draft": false`). A published release keeps its tag and stays published.
- **Pre-releases** are never `latest`.
- **Notes:** `generate-notes` lists the pull requests merged since the previous release (or `previous_tag_name`).
- **Webhooks:** GitHub's `release` event (created, published, released, prereleased, edited, deleted), with the assets. A tag created by a release is also a `push` event.
- **Web UI:** a Releases tab (with the count) lists releases with their notes, files, download counts and source code archives; the code page links the latest release. Writers draft releases on a new or existing tag, generate notes, preview them, and attach files on the release page (with progress, refused above the limit). Protected tags are in the repository settings, after branch protection.

#### Assets

```bash
# Upload: the file is the body (streamed), ?name= its file name
curl -X POST "localhost:8080/api/v1/repository/me%2Fmy-project/releases/$RELEASE/assets?name=app_linux_amd64.tar.gz&label=Linux" \
  -H "Authorization: token $CI_TOKEN" -H 'Content-Type: application/gzip' --data-binary @dist/app_linux_amd64.tar.gz
# Download from a published release (git's auth: a token for private repositories)
curl -LO localhost:8080/me/my-project/releases/download/v1.2.0/app_linux_amd64.tar.gz
```

- Writers upload, rename and delete assets, on drafts too. Names are made safe (other characters become `.`) and unique per release.
- At most `releases.max_asset_size` bytes (100 MB by default) per asset. Uploads are streamed to the repository's storage (local or S3), never held in memory, and refused once over the limit.
- Downloads always come as `application/octet-stream` attachments, whatever the uploaded type, and each one is counted (`download_count`). Drafts' assets are only downloadable by writers, through the API (`.../assets/<id>/download`).
- Deleting a release deletes its assets; deleting the repository deletes everything.

#### GitHub-compatible API (goreleaser and the like)

`/api/v3/repos/<owner>/<repo>/releases` answers GitHub's releases endpoints: list, get, by tag, latest, create (with `generate_release_notes`), edit, delete, generate notes, and assets (list, get, download with `Accept: application/octet-stream`, edit, delete). Uploads go to the `upload_url` of a release, under `/api/uploads` (GitHub Enterprise's upload URL). Ids are numbers there, since GitHub's clients parse them as integers. Authenticate with a personal access token or a repository token. For goreleaser:

```yaml
github_urls:
  api: https://git.example.com/api/v3/
  upload: https://git.example.com/api/uploads/
  download: https://git.example.com/
```

#### Protected tags

```bash
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/protected-tags -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"pattern": "v*", "allowed_users": ["release-bot"]}'
```

- Owners manage the rules; developers and up can read them. `pattern` is a tag name or a glob (`*` within a segment, `**` across, `?`).
- Only maintainers and up, and the users and groups listed, may create a matching tag, by a push or by publishing a release. Deploy keys and repository tokens never may; they can still release an existing protected tag.
- Nobody may move or delete a protected tag, except owners, and every such bypass is logged (`"component":"audit"`).

### Importing and mirroring

Create a repository from any git remote: on the "New repository" page choose "Import from a URL", or `POST /api/v1/repository/import` with `name`, `url` and optionally `namespace`, `username`, `password` (a password or token), `mirror`, `lfs`, `interval_seconds`.

- **Remotes:** `https://` or `http://` (with an optional username and password or token), or `ssh://` and `git@host:path`: oGit generates a key for the repository and shows its public key, to add on the remote (for instance as a deploy key); the remote's host key is pinned on first contact. Credentials and keys are stored encrypted (they need `security.encryption_key`) and never shown again.
- **Import or mirror:** an import copies the branches and tags once and is a normal repository afterwards. A mirror keeps following the remote: every 8 hours by default (`interval_seconds`, at least `mirrors.min_interval`), branches and tags deleted upstream are deleted too, and it refuses pushes and merges until an owner stops mirroring. Maintainers see its status and last error, change it and "Sync now"; failed syncs retry with backoff.
- **Git LFS:** with `lfs`, the LFS objects the fetched branches and tags point to are fetched from the remote's LFS server too (over SSH through `git-lfs-authenticate`).
- **Safety:** like webhooks, mirrors can't reach private, loopback or link-local addresses unless listed in `mirrors.allowed_hosts`.
- **API:** `GET|PATCH|DELETE /api/v1/repository/<repo>/mirror` (`DELETE` stops mirroring, owners only), `POST .../mirror/sync`.

**Issues, pull requests and releases** can come along from GitHub (and GitHub Enterprise), GitLab or Gitea/Forgejo: tick "Also import issues, pull requests, labels, milestones and releases" on the import page, or add `forge` (`github`, `gitlab` or `gitea`) and optionally `api_token` (default: the password) and `api_url` (default: worked out from the remote URL) to `POST /api/v1/repository/import`.

- **What:** labels, milestones, issues and pull requests with their comments, reviews (inline comments become comments quoting the file and line), assignees and dates, and releases with their assets (up to `releases.max_asset_size`; GitLab's asset links stay links in the notes). It runs once, in the background, after the code (which brings the pull requests' heads along); `GET /api/v1/repository/<repo>/import` shows the progress, warnings and errors.
- **Numbers** are kept, and issues opened meanwhile come after them. GitLab numbers merge requests apart from issues, so its merge requests come after the issues here (each says which `!n` it was).
- **People:** authors, assignees and mergers are matched to users here by user name (or public email). Anyone else's issues and comments appear under the importer's name with a line "Originally by @login on GitHub, date", and their reviews become comments (an approval never counts as the importer's).
- **Open pull requests** stay open: mergeable when their source branch is here, read-only with their recorded diff otherwise (pull requests from forks show `owner:branch`).
- **Interruptions:** rate limits pause the import until the forge's reset; failures retry with backoff, then wait for "Resume" (`POST .../import/retry`); nothing is imported twice. Owners cancel it with `DELETE .../import`. The token is stored encrypted and forgotten once done. No notifications, webhooks or Actions runs are sent.

**Push mirrors** go the other way: the repository settings' "Push mirrors" section (or `POST /api/v1/repository/<repo>/push-mirrors` with `url`, `username`, `password`, `mode`, `lfs`, `interval_seconds`) adds up to 10 remotes per repository, managed by maintainers and owners.

- **When:** within seconds of every push (git pushes, merges, release tags, fork syncs, and pull mirror syncs, so a repository can relay another), and every 8 hours by default. Pushing never waits for them, and failures retry with backoff.
- **Mode:** `mirror` (the default) force-pushes and deletes the remote's branches and tags that this repository doesn't have; `branches` never forces or deletes, pushes what it can and reports the diverged branches and moved tags in the last error. Other refs on the remote are never touched, and a repository without branches pushes nothing.
- **Remotes and credentials** work like pull mirrors' (an SSH key needs write access on the remote, e.g. a deploy key allowed to push), and so does the address check (`mirrors.allowed_hosts`). With `lfs`, the LFS objects the pushed branches and tags point to are uploaded to the remote's LFS server first.
- **API:** `GET|POST /api/v1/repository/<repo>/push-mirrors`, `PATCH|DELETE .../push-mirrors/<id>`, `POST .../push-mirrors/<id>/sync`.

### Forks

```bash
# Fork into your namespace (or {"namespace": "<group you maintain>", "name": "..."})
curl -X POST localhost:8080/api/v1/repository/alice%2Fapp/forks -H "Authorization: Bearer $SESSION"
# Open a pull request from your fork into the original
curl -X POST localhost:8080/api/v1/repository/alice%2Fapp/pulls -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"title": "Fix", "source": "fix", "source_repository": "bob/app"}'
# Fast-forward your fork's branch to the original's
curl -X POST localhost:8080/api/v1/repository/bob%2Fapp/sync -H "Authorization: Bearer $SESSION" -d '{"branch": "main"}'
```

- **Copies:** a fork is a full copy of the original's branches and tags. Deleting the original, or making it private, never affects the fork.
- **Privacy:** anyone who can read a repository can fork it, into their own namespace or a group they maintain. Forks of private repositories are private, and the original's members don't get access to them. `GET .../forks` lists the forks you can read, and `fork_of` on a repository names the original when you can read it.
- **Pull requests from forks:** you need write access to the fork and read access to the original. The fork's commits are copied into the original as `refs/pull/<number>/head` when the pull request is opened and whenever the fork branch is pushed, so merged and closed pull requests stay readable after the fork is deleted. The original's maintainers merge, and "delete branch" never touches the fork. `GET .../pulls/preview?source_repository=bob/app&source=fix` compares before opening.
- **Maintainer edits:** with `allow_maintainer_edits` (on by default, set by the author), the original's developers may push to the fork's source branch, and only that branch, while the pull request is open, over HTTP or SSH.
- **Sync:** fast-forwards a fork's branch when it has no commits of its own, otherwise answers `DIVERGED`.
- **Webhooks:** GitHub's `fork` event goes to the original's hooks.

### Wikis

Every repository has a wiki: Markdown pages kept in a git repository of their
own, `<owner>/<repo>.wiki.git`, which is created by the first edit or push.

In the web UI, the repository's **Wiki** tab lists the pages, renders them
with their sidebar and footer, and lets writers create, edit (with a preview
and file attachments), rename and delete pages, browse each page's history and
changes, revert to an older version, and search. `[[Page Name]]` and
`[[text|Page Name]]` link pages; links to pages that don't exist yet are red
and open the new page form. Maintainers choose who edits in **Settings**.

```bash
# Create, read, edit (base: the head the page was read at), rename, delete
curl -X POST localhost:8080/api/v1/repository/alice%2Fapp/wiki/pages -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "Getting Started", "content": "# Hello"}'
curl 'localhost:8080/api/v1/repository/alice%2Fapp/wiki/page?name=Getting-Started'
curl -X PUT localhost:8080/api/v1/repository/alice%2Fapp/wiki/page -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "Getting-Started", "content": "# Hello again", "base": "<head>"}'
# Search, revert a page to a commit, upload an attachment
curl 'localhost:8080/api/v1/repository/alice%2Fapp/wiki/search?q=hello'
curl -X POST localhost:8080/api/v1/repository/alice%2Fapp/wiki/revert -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "Getting-Started", "version": "<commit>"}'
curl -X POST 'localhost:8080/api/v1/repository/alice%2Fapp/wiki/uploads?name=diagram.png' -H "Authorization: Bearer $SESSION" --data-binary @diagram.png
# Or with git
git clone http://localhost:8080/alice/app.wiki.git
```

- **Who:** anyone who can read the repository reads its wiki. Developers and up edit it, by default. Maintainers can let every signed-in reader edit it (`"wiki_access": "readers"` on `PATCH /api/v1/repository/<id>`) or turn it off (`"off"`: hidden everywhere, content kept). Archived repositories' and mirrors' wikis are read-only.
- **Pages** are `.md` files (`.markdown` is read too) on the wiki's default branch, in folders if you like. A page's name is its path without the extension, and its title is the last segment with dashes shown as spaces (`Docs/Getting-Started`). `Home` is the front page. `_Sidebar` and `_Footer` are shown with every page; the nearest one wins, from the page's folder up.
- **Edits** are commits by the editor. An edit names the head it started from (`base`), and fails with `WIKI_CONFLICT` if that page changed since. Edits of other pages in the meantime don't conflict. Without `base`, an edit overwrites the page.
- **History and diffs** use the browse API on `<owner>%2F<repo>.wiki`, e.g. `/api/v1/repository/alice%2Fapp.wiki/commits?spec=main/Home.md`. Raw files are at `/alice/app.wiki/raw/<ref>/<path>`.
- **Attachments** (25 MiB each) are stored as Git LFS objects and committed as pointers under `uploads/`, with a `.gitattributes` so clones get the files. Raw links serve them, never as HTML.
- **Git:** `<path>.wiki.git` over HTTP and SSH, with the repository's tokens and keys and the same roles. Branch protection and LFS locks are the repository's and don't apply to its wiki.
- **Events:** each change (UI edit, or push to the wiki's default branch) sends GitHub's `gollum` webhook event, starts workflows with `on: gollum`, and notifies the repository's watchers at "all activity".
- **Copies:** forks copy the wiki (unless it's off). Pull mirrors and imports fetch the remote's `<url>.wiki.git` when it has one, and push mirrors push to the remote's wiki, best effort (forges usually create a wiki only from their UI). Renames and transfers keep it; deleting the repository deletes it.

### Issues

```bash
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/issues -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"title": "Login fails on Safari", "body": "Steps: ..."}'
curl "localhost:8080/api/v1/repository/me%2Fmy-project/issues?state=open&label=bug&q=safari"
curl -X PATCH localhost:8080/api/v1/repository/me%2Fmy-project/issues/12 -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"state": "closed"}'
```

- **Numbers** are shared with pull requests, so `#12` is one or the other. `GET .../numbers/12` says which.
- **Who:** anyone who can read the repository opens issues and comments. The author and developers and up edit, close and reopen. Developers and up set labels (the same labels as pull requests). Maintainers and up delete issues and others' comments.
- **Filters:** `?state=open|closed|all`, `?label=`, `?author=`, `?q=` (title and body). Counts per state follow the other filters.
- **Assignees and milestones** (issues and pull requests): developers and up set them with `PUT .../issues/12/assignees` (`{"assignees": ["alice"]}`, users with write access, at most 10) and `PUT .../issues/12/milestone` (`{"milestone_id": "..."}`; same paths under `pulls`). Maintainers and up manage milestones at `.../milestones` (title, description, `due_on`, `state`). Each milestone counts its open and closed issues and pull requests.
- **"Fixes #12"** (also close/closes/closed/fix/fixed/resolve/resolves/resolved) in a pull request's title or body closes the issue when the pull request is merged into the default branch.
- **More filters:** `?assignee=<user>|none`, `?milestone=<id>|none`, `?mine=assigned|created`.
- **Email:** when SMTP is configured, the author, assignees, commenters and @mentioned users (if they can read the repository) hear about others' activity.
- **Webhooks:** GitHub's `issues` event (opened, edited, closed, reopened, labeled, unlabeled, assigned, unassigned, milestoned, demilestoned, deleted), `issue_comment` for issue comments (the issue has no `pull_request` field), and `milestone` (created, edited, closed, opened, deleted). Pull requests also send assigned, unassigned, milestoned and demilestoned.
- **Off switch:** owners set `"has_issues": false` on the repository. Existing issues stay readable, and nothing can be added or changed until issues are turned back on.

### Branch protection

Owners protect branches in the repository's **Settings** page (which offers to
protect the default branch) or through the API, by name or glob pattern (`main`, `release/*`, `hotfix/**`;
`*` stays within a path segment, `**` crosses them). When several rules match a
branch, the most specific one applies.

```bash
curl -X POST localhost:8080/api/v1/repository/me%2Fmy-project/branch-protection -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' \
  -d '{"pattern": "main", "require_pull_request": true, "required_approvals": 2, "dismiss_stale_approvals": true, "allowed_groups": ["release-team"]}'
```

- **Always:** a protected branch refuses force-pushes and deletion, unless the rule sets `allow_force_push` or `allow_deletion`. A push that breaks a rule is refused for that branch only; git shows the reason.
- `require_pull_request`: no direct pushes. The branch only moves by merging pull requests (pushing it into existence is still allowed).
- `required_approvals` (`-1` or omitted: the repository's setting) and `dismiss_stale_approvals` (only approvals of the current head commit count) apply to merges into the branch.
- `allowed_users` / `allowed_groups`: when set, only they may push to the branch or merge into it. They still need write access; deploy keys and repository tokens are never on the list.
- `owners_bypass`: repository owners may break the rule. Each bypass is logged with `"component":"audit"`.
- A pull request's `merge_blocked` says why the caller can't merge it, and `protected_by` names the rule. A protected source branch is kept after a merge, even with `delete_branch`.

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

- **Events:** `gollum` (wiki pages created or edited, see [Wikis](#wikis)), `status` (commit statuses), `push` (the default; HTTP, SSH and pull request merges, including deleted branches), `repository` (renamed, transferred, publicized, privatized, archived, unarchived, edited), or `*` for all. A `ping` is sent when a hook is created.
  - `pull_request`: opened, edited, closed (with `merged: true` for merges), reopened, synchronize (a push to the source branch), converted_to_draft, ready_for_review, labeled, unlabeled.
  - `pull_request_review`: submitted. `pull_request_review_comment`: inline comments created, edited, deleted (also those of a review).
  - `issue_comment`: conversation comments created, edited, deleted; for pull requests the `issue` has a `pull_request` field, as on GitHub.
  - `issues` and `milestone`: see [Issues](#issues).
  - Deliveries are sent in parallel, so their order is not guaranteed.
- **Signing:** with a secret, `X-Hub-Signature-256` is `sha256=` plus the HMAC-SHA256 of the body. Secrets are write-only.
- **Delivery:** events are queued in the database and sent in the background by every server, each delivery by one of them. A 2xx answer is a success; anything else is retried after 30s, 2m, 10m and 30m, then marked failed. Redirects are not followed.
- **Private networks:** targets on private, loopback or link-local addresses are refused unless listed in `webhooks.allowed_hosts`.
- **URLs** in payloads are built from `http.public_url`; set it.

### Namespaces, members and groups

Every repository belongs to a user or a group, its namespace. Usernames and
group names are one set of names (lowercase letters, digits, `.`, `_`, `-`;
not ending in `.git`; a few words such as `admin`, `api`, `explore`,
`settings` are reserved), so `/<name>` is never ambiguous.

- **Your namespace:** you create repositories there by default and own them.
- **A group's namespace:** its owner and maintainers create repositories there with `{"namespace": "<group>", "name": ...}`. Group members get their group role on all of the group's repositories: the owner is owner, maintainers are maintainers, members are developers.
- A user or group that still has repositories in its namespace can't be deleted; move or delete them first.
- **Sub-groups:** a group's maintainers and owners create sub-groups in it (`{"name": "team", "parent": "org"}`), up to 5 levels deep: `org/team/app.git`, over HTTP and SSH alike (the Go proxy and GitHub's API take nested paths too). Members of a group are members of every sub-group below it with the same role; a sub-group adds its own members or raises an inherited member's role, never lowers it (`409 INHERITED_ROLE`). A group with sub-groups can't be deleted, and a sub-group can't share its path with a repository (`org/team` is one or the other). In the API, a group's path goes URL-encoded where one segment is expected: `/api/v1/group/org%2Fteam`, `/api/v1/namespace/org%2Fteam` (which lists the sub-groups you can see).
  - **Moving:** a group's owner renames a sub-group or moves a group under another one they maintain (`POST /api/v1/group/org%2Fteam/move` with `name` and/or `parent`; `"parent": ""` makes it top-level). Everything below moves along, and every former path (groups, repositories, git URLs) redirects until something takes it. Top-level groups can't be renamed (they share names with users), but can move under a group.
  - **Inherited settings:** a group that requires two-factor authentication requires it of every sub-group; group Actions runners serve the sub-groups' repositories; group secrets and variables reach them too (the nearest group wins, the repository's own win over all).
  - **Group webhooks** (`/api/v1/group/<group>/hooks`, the same API as a repository's, for maintainers and owners) fire for every repository in the group and below.
  - **Web UI:** sub-groups have pages like any group (`/org/team`, with breadcrumbs and their sub-groups), repositories inside them at `/org/team/app`, and the group's management page (`/groups/org/team`) lists and creates sub-groups, shows inherited members, moves or renames the group and manages its webhooks. Pages under a former path redirect after a move.
  - **Packages:** sub-groups own packages: images `org/team/app` (the deepest existing group is the owner), generic files under `/api/packages/org/team/generic/...`, and npm packages in their top-level group's scope, named after their path: `org/web/ui` publishes `@org/web-ui-<name>`. Names stay when a group moves.

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

## Web UI

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
