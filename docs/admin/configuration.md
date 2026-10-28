# Configuration

Every setting can come from three places, and the first one found wins:

1. a flag: `--http.port 9090`;
2. an environment variable: `HTTP_PORT=9090`;
3. the YAML file given with `-c` (default `config.yaml`; a missing file is ignored).

Environment variable names are the YAML key in capitals, with dots and dashes as underscores: `storage.s3.cache.max_mb` is `STORAGE_S3_CACHE_MAX_MB`. `ogit server --help` lists every flag with its variable. The exception is `sso.providers`, a list that is only read from the file (its `client_secret` values may name environment variables, `${OGIT_OIDC_SECRET}`).

## Before going to production

| Setting | Why |
|---|---|
| `session.secret_key` | Signs session tokens. At least 32 bytes (`openssl rand -hex 32`). Unset, a random key is made at each start and everyone is signed out on restart. |
| `security.encryption_key` | Encrypts secrets kept in the database: authenticator app seeds, SSO client secrets, mirror credentials, Actions secrets. At least 32 bytes. **Keep it**: a new key makes those secrets unreadable. |
| `http.public_url` | The address users reach, e.g. `https://git.example.com`. Used in emails, webhook payloads, SSO callbacks, passkeys, LFS over SSH and the Go module proxy. |
| `http.trusted_proxies` | Your reverse proxies, so client addresses are right. See [Behind a reverse proxy](./reverse-proxy). |
| `database.*` | PostgreSQL for more than one server. See [Database](./database). |
| `storage.*` | Where the git data lives. See [Storage](./storage). |
| `smtp.*` | Outgoing mail: verification links, notifications and security alerts. |

Keep both keys secret and in your [backups](./backups). Passing them as environment variables keeps them out of the file.

## Main settings

| YAML key | Environment | Default |
|---|---|---|
| `http.port` | `HTTP_PORT` | `8080` |
| `http.logs` | `HTTP_LOGS` | `false` (an access log line per request) |
| `http.public_url` | `HTTP_PUBLIC_URL` | none |
| `http.trusted_proxies` | `HTTP_TRUSTED_PROXIES` | none |
| `logger.level` / `logger.pretty` | `LOGGER_LEVEL` / `LOGGER_PRETTY` | `info` / `false` (JSON) |
| `database.dialect` / `database.dsn` | `DATABASE_DIALECT` / `DATABASE_DSN` | `sqlite` / `./database.sqlite` |
| `session.secret_key` | `SESSION_SECRET_KEY` | random per start |
| `session.expiration_minutes` | `SESSION_EXPIRATION_MINUTES` | `43200` (30 days) |
| `security.encryption_key` | `SECURITY_ENCRYPTION_KEY` | none |
| `registration.*` | `REGISTRATION_*` | open, see [Registration](./registration) |
| `ratelimit.*` | `RATELIMIT_*` | on, in memory, see [Rate limits](./rate-limits) |
| `smtp.host`, `.port`, `.username`, `.password`, `.from`, `.tls` | `SMTP_*` | port `587`, `starttls` |
| `ssh.enabled` / `ssh.port` / `ssh.host_key_path` | `SSH_*` | `false` / `2222` / `./ssh_host_ed25519_key` |
| `storage.type` | `STORAGE_TYPE` | `local` (or `s3`) |
| `webhooks.allowed_hosts` / `mirrors.allowed_hosts` | `WEBHOOKS_ALLOWED_HOSTS` / `MIRRORS_ALLOWED_HOSTS` | none: private addresses are refused |

### Limits

| YAML key | Default |
|---|---|
| `git.max_push_size` | 2 GB per push (0: no limit) |
| `lfs.max_object_size` | 2 GB per Git LFS object |
| `releases.max_asset_size` | 100 MB per release asset |
| `packages.max_file_size` | 2 GB per package file or container layer |
| `actions.max_artifact_size` / `actions.cache_size_limit` | 2 GB per artifact / 10 GB of cache per repository |
| `actions.artifact_retention_days` | 90 |
| `mirrors.min_interval` / `mirrors.default_interval` | `10m` / `8h` |

### Outgoing connections

Webhooks, pull mirrors and imports never connect to private, loopback, link-local or shared addresses. The check is made on the address actually dialed, so DNS tricks don't get around it. To reach an internal CI or git server, list it:

```yaml
webhooks:
  allowed_hosts: [ci.internal, 10.0.5.0/24]
mirrors:
  allowed_hosts: [git.internal]
```

## Every setting

`config-example.yaml` documents every key with its default:

<<< @/../config-example.yaml
