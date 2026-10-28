# Running oGit

oGit is a single binary. It has three parts:

- **The server** (`ogit server`): the web UI, the REST API, git over HTTP and, if enabled, git over SSH.
- **A database:** SQLite for a single server, or PostgreSQL. It holds users, permissions, pull requests, issues and settings.
- **Repository storage:** a directory, or an S3 bucket. It holds the git data, plus Git LFS objects, release assets, packages and Actions artifacts.

Several servers can share one PostgreSQL database and one S3 bucket. Put them behind a load balancer, and use Redis for [rate limits](./rate-limits) and the [S3 cache](./storage#cache).

## Where to start

1. [Install](./install) the binary or the Docker image, and create the first admin.
2. Write a [configuration](./configuration). At a minimum, set `session.secret_key`, `security.encryption_key` and `http.public_url`.
3. Choose a [database](./database) and [storage](./storage).
4. Put oGit [behind a reverse proxy](./reverse-proxy) with TLS.
5. Decide how people sign in: [registration](./registration), [two-factor authentication](./two-factor), [single sign-on](./sso).
6. Plan [backups](./backups) and [upgrades](./upgrades).

The [guides](/guides/) walk through common setups step by step.
