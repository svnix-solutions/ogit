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
- **Packages**: a container registry (`docker push` and `pull`) and generic files, owned by users and groups, linked to repositories and published from Actions
- **Releases**: on tags, with drafts, pre-releases, generated notes, uploaded assets and protected tags; GitHub's releases API for release tools
- **Forks**: fork any repository you can read, open pull requests from your fork, sync it with the original
- **Issues**: per repository, numbered with pull requests, with comments, labels, assignees, milestones and "Fixes #N"
- **Branch protection**: no force-pushes or deletion, pull-request-only branches, per-branch approvals, push and merge allowlists
- **Commit statuses**: CI reports pass or fail per commit (GitHub-compatible API), and protected branches can require checks
- **Notifications**: an inbox of the issues, pull requests and releases that concern you, repository watch levels, and email preferences
- **Webhooks**: GitHub-compatible push, pull request and repository events, signed and retried
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
- **Generic files:** `curl -u <user>:<token> --upload-file tool.tar.gz <url>/api/packages/<owner>/generic/<name>/<version>/tool.tar.gz`, downloaded at the same URL. A version's file is published once; downloads are always attachments.
- **Who publishes:** the namespace's user, a group's maintainers and owners, the linked repository's developers and up, and that repository's tokens (Actions jobs included, which create packages linked to their repository).
- **Visibility:** private by default; linked packages may follow their repository (public with it), or be made public. Readers of the linked repository read the package.
- **Limits:** files and layers up to `packages.max_file_size` (2 GB).
- **Deleting:** package admins (the namespace's user, group maintainers and owners, the linked repository's maintainers and owners) delete versions (for images: a digest; deleting a tag only untags) and packages. Layers no image uses any more are deleted after a day. Deleting a repository unlinks its packages and makes them private; users and groups with packages can't be deleted.
- **Web UI:** a namespace's page lists its packages, repositories with linked packages get a Packages tab, and a package's page shows its versions (tags, files), install command and settings.
- **API:** `GET /api/v1/packages/<owner>[?type=]`, `GET|PATCH|DELETE /api/v1/packages/<owner>/<type>/<name>` (image names with slashes URL-encoded; `DELETE` takes `?confirm=<name>`), `DELETE .../versions/<version>`, `GET /api/v1/repository/<owner>%2F<repo>/packages`.

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

- **Events:** `status` (commit statuses), `push` (the default; HTTP, SSH and pull request merges, including deleted branches), `repository` (renamed, transferred, publicized, privatized, archived, unarchived, edited), or `*` for all. A `ping` is sent when a hook is created.
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
./ogit admin reset-2fa -c config.yaml --username someone   # lost device and recovery codes
# Docker: docker exec <container> ./app admin promote -c /config/config.yaml --email you@example.com
```

Admins use `/api/v1/admin` (`/documentation` lists the details):

| Endpoint | |
|---|---|
| `GET /api/v1/admin/users?q=&limit=&offset=` | list and search users |
| `POST /api/v1/admin/users` | create a verified account with an initial password (works when registration is off) |
| `PATCH /api/v1/admin/users/<username>` | `{"active": false}` disables the account and ends its sessions immediately; also `"email_verified": true`, `"role": "admin"` or `"user"` |
| `DELETE /api/v1/admin/users/<username>` | delete a user and their sessions, tokens and memberships |
| `POST /api/v1/admin/users/<username>/reset-2fa` | turn off a user's two-factor authentication (they're emailed and can set it up again) |
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
| `http.public_url` | `HTTP_PUBLIC_URL` | none; needed for email links and webhook payloads |
| `http.trusted_proxies` | `HTTP_TRUSTED_PROXIES` | none; see [Rate limits](#rate-limits) |
| `ratelimit.*` | `RATELIMIT_*` | enabled, in memory; see [Rate limits](#rate-limits) |
| `smtp.host`, `.port`, `.username`, `.password`, `.from`, `.tls` | `SMTP_*` | port `587`, tls `starttls` |
| `webhooks.allowed_hosts` / `webhooks.timeout` | `WEBHOOKS_ALLOWED_HOSTS` / `WEBHOOKS_TIMEOUT` | none / `10s`; see [Webhooks](#webhooks) |
| `ssh.enabled` / `ssh.port` / `ssh.host_key_path` | `SSH_ENABLED` / `SSH_PORT` / `SSH_HOST_KEY_PATH` | `false` / `2222` / `./ssh_host_ed25519_key` |
| `storage.type` | `STORAGE_TYPE` | `local` (or `s3`) |
| `storage.local.path` | `STORAGE_LOCAL_PATH` | `./repositories` |
| `storage.s3.bucket`, `.endpoint`, `.accesskey`, `.secretkey`, `.region` | `STORAGE_S3_*` | region `us-east-1` |
| `storage.s3.cache.type` / `.max_mb` / `.redis_url` | `STORAGE_S3_CACHE_*` | `memory` / `256` / none; see [S3 storage](#s3-storage) |

`./ogit server --help` lists every flag.

### Rate limits

Enabled by default (`config-example.yaml` lists every rule):

- **Password and token guessing:** 10 failed logins, password changes or git token checks per IP in 15 minutes, and 50 failed logins per account from any IP. Successful logins don't count. After that, even the right password gets `429` until the window ends (`Retry-After` says when).
- **Registration** and verification emails: 5 per IP per hour.
- **API:** 600 requests a minute per signed-in user, 120 per IP without a session (`RateLimit-*` headers show what's left).
- **Git** (HTTP, SSH, raw files and archives): 300 operations a minute per token, user or SSH key; 60 per IP without credentials.

Counters live in memory, so each server counts on its own; with
`ratelimit.store: redis` all servers share them. If Redis is down, requests
are allowed (and the error logged).

Behind a reverse proxy, list it in `http.trusted_proxies`: otherwise every
request seems to come from the proxy, and they all share one budget.
`X-Forwarded-For` (and `X-Forwarded-Proto`) are only believed from those
addresses, read from the right, so a client can't choose its own address.

### Two-factor authentication

Users turn it on in their settings (or `POST /api/v1/user/2fa/totp`, then
`/api/v1/user/2fa/totp/confirm` with a first code) with any authenticator app.
They get 10 single-use recovery codes, shown once. The server needs
`security.encryption_key` (at least 32 bytes; it encrypts the app secrets): keep
it, since changing it turns app codes off for everyone (recovery codes still
work).

- **Security keys and passkeys** (WebAuthn) are added in the settings too; they need `http.public_url`, whose host is the relying party, so keys only work on that address. A key added as a passkey verifies the user (PIN, biometrics) and can also sign in without the password ("Sign in with a passkey"), which counts as both factors. The first second factor (app or key) comes with the recovery codes; removing the last one turns 2FA off. A key whose signature counter goes backwards (a clone) is refused.
- Signing in with 2FA: `POST /api/v1/auth/login` answers `{"two_factor_required": true, "challenge": "…", "methods": ["totp", "webauthn"]}`. With a key, `POST /api/v1/auth/login/2fa/webauthn {"challenge"}` returns WebAuthn options, and the browser's answer goes to `/auth/login/2fa` as `credential` with the `ceremony`. Send `POST /api/v1/auth/login/2fa {"challenge": "…", "code": "123456"}` (or a recovery code) within 5 minutes; 5 wrong codes end the attempt, and wrong codes count as failed logins. The web UI does the same with `/auth/session` and `/auth/session/2fa`.
- **Sudo mode:** turning 2FA off, new recovery codes, new access tokens, SSH and deploy keys, repository tokens, and resetting someone's 2FA need a session confirmed in the last 10 minutes. Otherwise they answer 403 `SUDO_REQUIRED`. Signing in counts; `POST /api/v1/auth/sudo` confirms again with `{"code": …}` (or `{"password": …}` without 2FA). The web UI asks automatically.
- Access tokens and SSH keys keep working for git and CI without codes.
- Users are emailed (when SMTP is set) when 2FA is turned on or off, or reset by an admin.
- **Requiring it:**
  - site-wide with `security.require_2fa: true`, or from the admin page (`PATCH /api/v1/admin/settings {"require_two_factor": true}`). Users without 2FA can still sign in, but only reach its setup until they add an app or a key; the API answers 403 `TWO_FACTOR_REQUIRED`, and their access tokens and SSH keys are refused. Repository tokens and deploy keys keep working;
  - per group, by its owner (`PATCH /api/v1/group/<name> {"require_two_factor": true}`): members without 2FA get no access to the group's repositories through the group, and can't create repositories in it, until they set it up. They stay members; owners and maintainers see who lacks 2FA;
  - site admins always need 2FA for admin actions (`ADMIN_TWO_FACTOR_REQUIRED`). `ogit admin` on the server is not affected.
- Lost device and recovery codes: an admin resets it (`POST /api/v1/admin/users/<username>/reset-2fa`, or `ogit admin reset-2fa`).

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
