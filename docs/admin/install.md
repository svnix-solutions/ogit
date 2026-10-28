# Installation

## Docker

To look around first, [try oGit with Docker Compose](/guides/try-it): one command starts it with PostgreSQL, S3 and Redis.

Release images are published to `ghcr.io/svnix-solutions/ogit` for every `v*` tag. The container reads `/config/config.yaml`, runs the database migrations, then starts the server:

```bash
docker run -p 8080:8080 -p 2222:2222 \
  -v "$PWD/config.yaml:/config/config.yaml:ro" \
  -v "$PWD/data:/data" \
  ghcr.io/svnix-solutions/ogit:<tag>
```

Keep everything that must survive the container on the `/data` volume:

```yaml
database:
  dsn: /data/database.sqlite      # or use PostgreSQL
storage:
  local:
    path: /data/repositories      # or use S3
ssh:
  host_key_path: /data/ssh_host_ed25519_key
```

See [Run oGit with Docker and PostgreSQL](/guides/docker-postgres) for a complete setup.

## From source

Building needs Go (the version in `go.mod`) and a C toolchain, because the SQLite driver uses CGO. It also needs Node.js (the version in `web/.nvmrc`) for the web UI.

```bash
git clone https://github.com/svnix-solutions/ogit.git && cd ogit
npm --prefix web ci && npm --prefix web run build   # the web UI, embedded in the binary
go build -o ogit ./cmd/cmd.go

cp config-example.yaml config.yaml   # then edit it
./ogit migration -c config.yaml      # create or upgrade the database schema
./ogit server -c config.yaml         # listens on :8080 by default
```

The web UI is at `http://localhost:8080/`. A binary built without the UI step still works, and its home page explains how to build the UI. The API documentation is at `/documentation`.

::: warning Run the migrations
Run `ogit migration` before the first start and after every upgrade. The server doesn't migrate the database itself: without the migrations it starts, but API calls fail on missing tables. The Docker image runs them on every start.
:::

## The first admin

Register an account in the web UI, then make it a site admin from the server. The command writes to the database directly, like `migration`:

```bash
./ogit admin promote -c config.yaml --email you@example.com   # or --username
# Docker:
docker exec <container> ./app admin promote -c /config/config.yaml --email you@example.com
```

If registration is off (`registration.enabled: false`), turn it on for the first account, or let a [single sign-on](./sso) provider declared in the configuration create it on first sign-in. After that, admins create accounts with the [admin API](./administration).

## Commands

| Command | |
|---|---|
| `ogit server` | the server |
| `ogit migration` | create or upgrade the database schema |
| `ogit admin promote\|demote\|reset-2fa` | [site admins](./administration#from-the-server) and 2FA resets |
| `ogit storage gc\|prune\|repack` | [S3 maintenance](./storage#maintenance) |
| `ogit runner register\|run` | an Actions runner, on the machines that run jobs |

Each takes `-c <config file>`, and `--help` lists its flags.
