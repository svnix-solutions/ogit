# Try oGit with Docker Compose

The repository's `docker-compose.yaml` starts a complete oGit on your machine: PostgreSQL, S3 storage (MinIO), Redis, oGit itself, an Actions runner, and a demo user with a repository to look at. You need Docker with Compose and git.

```bash
git clone https://github.com/svnix-solutions/ogit.git && cd ogit
docker compose up -d --build     # the first build takes a few minutes
docker compose logs seed         # how to sign in
```

Open http://localhost:8080 and sign in as `demo@example.com` with the password from the `seed` logs. It is generated once and kept in the `ogit-data` volume.

::: warning A showcase, not a production setup
The keys and passwords in `docker-compose.yaml` are public. To run oGit for real, follow [Run oGit with Docker and PostgreSQL](./docker-postgres) and the [administration docs](/admin/).
:::

## What's running

| Service | | Port |
|---|---|---|
| `ogit` | the web UI, the API, git over HTTP and SSH | 8080, 2222 |
| `db` | PostgreSQL | 5432 |
| `redis` | Redis: rate limit counters and the S3 pack cache | |
| `s3` | MinIO, the S3 storage; its console shows the `ogit` bucket | 9001 |
| `s3-init` | creates the bucket, then exits | |
| `seed` | creates the demo user and repository, and a reusable runner registration token, once, then exits | |
| `runner` | runs `demo/showcase`'s Actions workflows in Docker, through your machine's Docker; `docker compose up -d --scale runner=3` for three | |

oGit reads `config-example.yaml`, the documented defaults (SQLite and local storage). The `x-ogit-environment` block in `docker-compose.yaml` sets what differs as environment variables, which override the file. See [Configuration](/admin/configuration).

The public URL is `http://localhost:8080`. Inside Docker, `localhost` is each container itself, so the runner's jobs reach the server on the Compose network instead, at `http://ogit:8080`: the runner is started with `--job-url http://ogit:8080`.

The image is `ghcr.io/svnix-solutions/ogit:${OGIT_TAG:-latest}`. `--build` builds it from the checkout with the repository's `Dockerfile`. To use a released image instead, run `OGIT_TAG=<version> docker compose up -d` without `--build`.

## Things to try

- **Browse** `demo/showcase`: its README, issue #1 and the wiki.
- **Clone and push over HTTP:** create an access token in **Settings → Access tokens** (`read:repository` and `write:repository`), then:

  ```bash
  git clone http://localhost:8080/demo/showcase.git
  cd showcase && echo hello > hello.txt && git add . && git commit -m "Say hello"
  git push    # user name: the token's name, password: the token
  ```

- **Over SSH:** add your public key in **Settings → SSH keys**, then `git clone ssh://git@localhost:2222/demo/showcase.git`.
- **Pull requests:** push a branch and open a pull request from the repository's **Pull requests** tab.
- **Actions:** add a workflow to `demo/showcase` and push it:

  ```yaml
  # .ogit/workflows/ci.yaml
  name: CI
  on: [push]
  jobs:
    hello:
      runs-on: ubuntu-latest
      steps:
        - uses: actions/checkout@v4
        - run: ls && echo "Hello from $GITHUB_REPOSITORY"
  ```

  The repository's **Actions** tab shows the run and its log. The runner uses your machine's Docker, so only run workflows you trust.
- **Admin:** `demo` is a site admin. The admin page asks for two-factor authentication first; any authenticator app works.
- **Storage:** sign in to the MinIO console at http://localhost:9001 (`ogit-showcase` / `ogit-showcase-secret`) and open the `ogit` bucket. Each repository is under `repositories/<id>.git`, its wiki under `<id>.wiki.git`.

## Stop or reset

```bash
docker compose stop        # stop, keep the data
docker compose down        # remove the containers, keep the data
docker compose down -v     # remove everything, the demo user and its password included
```

For development, `docker compose up db` starts only PostgreSQL on port 5432.
