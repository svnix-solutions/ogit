# Run oGit with Docker and PostgreSQL

This guide sets up oGit on one host with Docker Compose: PostgreSQL for the database, a volume for the repositories, and [Nginx Proxy Manager](https://nginxproxymanager.com/) in front of it for both HTTPS (a proxy host, with TLS) and git over SSH (a stream). Replace `git.example.com` with your host name, and point its DNS at the host.

## 1. Generate the keys

```bash
mkdir ogit && cd ogit
cat > .env <<EOF
SESSION_SECRET_KEY=$(openssl rand -hex 32)
SECURITY_ENCRYPTION_KEY=$(openssl rand -hex 32)
POSTGRES_PASSWORD=$(openssl rand -hex 16)
EOF
chmod 600 .env
```

Keep `.env` safe and in your backups: `SECURITY_ENCRYPTION_KEY` encrypts secrets in the database.

## 2. Write the configuration

`config.yaml`:

```yaml
http:
  port: 8080
  public_url: https://git.example.com
  trusted_proxies: ["172.16.0.0/12"]   # Docker's networks, where Nginx Proxy Manager is

database:
  dialect: postgres
  # The password comes from DATABASE_DSN in docker-compose.yaml.

storage:
  type: local
  local:
    path: /data/repositories

ssh:
  enabled: true
  port: 2222
  host_key_path: /data/ssh_host_ed25519_key

registration:
  enabled: true   # turn off once your users are in, if you like
```

`trusted_proxies` lets oGit believe the client address that Nginx Proxy Manager forwards. Without it, every request seems to come from the proxy and they all share one [rate limit](/admin/rate-limits) budget.

## 3. Write the Compose file

`docker-compose.yaml`:

```yaml
services:
  db:
    image: postgres:17
    restart: unless-stopped
    environment:
      POSTGRES_USER: ogit
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ogit
    volumes:
      - db:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "ogit"]
      interval: 5s

  ogit:
    image: ghcr.io/svnix-solutions/ogit:<tag>
    restart: unless-stopped
    depends_on:
      db:
        condition: service_healthy
    environment:
      SESSION_SECRET_KEY: ${SESSION_SECRET_KEY}
      SECURITY_ENCRYPTION_KEY: ${SECURITY_ENCRYPTION_KEY}
      DATABASE_DSN: host=db user=ogit password=${POSTGRES_PASSWORD} dbname=ogit port=5432 sslmode=disable
    volumes:
      - ./config.yaml:/config/config.yaml:ro
      - data:/data
    # No ports: HTTP and SSH both go through the proxy.

  proxy:
    image: jc21/nginx-proxy-manager:latest   # pin a version in production
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "2222:2222"         # git over SSH, forwarded to oGit by a stream (step 6)
      - "127.0.0.1:81:81"   # the admin UI: local only (reach it through an SSH tunnel)
    volumes:
      - npm-data:/data
      - npm-letsencrypt:/etc/letsencrypt

volumes:
  db:
  data:
  npm-data:
  npm-letsencrypt:
```

oGit publishes no ports: the proxy reaches it on the Compose network as `ogit:8080` (HTTP) and `ogit:2222` (SSH). Nginx Proxy Manager can only listen on ports published on its own container, so every port you forward must be listed under `proxy`.

## 4. Start it

```bash
docker compose up -d
docker compose logs -f ogit    # the migrations, then the server starting
```

The container runs the migrations on every start.

## 5. Set up the proxy host

Open the Nginx Proxy Manager admin UI. Port 81 is only bound to the host's loopback, so tunnel to it from your machine:

```bash
ssh -L 8181:127.0.0.1:81 you@your-host   # then open http://localhost:8181
```

Sign in and set up the admin account as it asks. Then **Hosts → Proxy Hosts → Add Proxy Host**:

**Details** tab:

| Field | Value |
|---|---|
| Domain Names | `git.example.com` |
| Scheme | `http` |
| Forward Hostname / IP | `ogit` |
| Forward Port | `8080` |
| Block Common Exploits | off (its URL patterns can reject legitimate paths) |
| Websockets Support | off (not used) |

**SSL** tab: **Request a new SSL Certificate** (Let's Encrypt), and turn on **Force SSL** and **HTTP/2 Support**. HSTS is optional.

**Advanced** tab, in **Custom Nginx Configuration**:

```nginx
# Pushes, LFS objects, release assets and packages can be large (oGit
# enforces its own limits), and are streamed, not buffered.
client_max_body_size 0;
proxy_request_buffering off;
proxy_buffering off;

# Big clones and pushes take minutes; Actions runners long-poll.
proxy_read_timeout 600s;
proxy_send_timeout 600s;
```

Save. Nginx Proxy Manager already sends `X-Forwarded-For` and `X-Forwarded-Proto`, which oGit needs.

::: warning The Advanced settings matter
Without them, large pushes and uploads can be refused (`HTTP 413`, `RPC failed`) or buffered to disk first, and long clones and pushes can be cut off by nginx's default 60-second timeouts.
:::

Check it:

```bash
curl https://git.example.com/api/health
```

## 6. Forward SSH with a stream

SSH isn't HTTP, so it doesn't go through a proxy host. A **stream** forwards the TCP connection as it is. In the admin UI, **Hosts → Streams → Add Stream**:

| Field | Value |
|---|---|
| Incoming Port | `2222` |
| Forward Host | `ogit` |
| Forward Port | `2222` |
| TCP Forwarding | on |
| UDP Forwarding | off |

If your version offers an SSL certificate for streams, leave it off. SSH encrypts the connection itself, and git clients don't expect TLS around it.

Check it:

```bash
ssh -p 2222 git@git.example.com
# Hi <you>! You've successfully authenticated, but oGit does not provide shell access.
```

That answer needs an SSH key added in your oGit settings; before that, the server refuses the key. Clone URLs look like `ssh://git@git.example.com:2222/<owner>/<repo>.git`. Allow port 2222 in your firewall.

Notes:

- **Client addresses:** a TCP stream doesn't pass on the client's address, so oGit sees the proxy's address for every SSH connection. SSH rate limits count per key, so they still work; only the addresses in the logs are lost.
- **Host key:** the proxy only relays bytes, so clients still check oGit's own host key (`ssh.host_key_path`), and moving SSH behind the proxy doesn't make them warn.
- **Port 22:** to use the standard port, the host's own SSH server must move to another port first: two services can't listen on the same port.

::: tip Simpler: publish SSH directly
If you don't need SSH to go through the proxy, skip the stream: remove `2222:2222` from `proxy` and add it to `ogit` instead (`ports: ["2222:2222"]`). oGit then also sees SSH clients' real addresses.
:::

## 7. Create the first admin

Register at `https://git.example.com/register`, then:

```bash
docker compose exec ogit ./app admin promote -c /config/config.yaml --email you@example.com
```

Sign in again, set up two-factor authentication (admin actions need it), and open `/admin`.

## Next steps

- Outgoing mail: add `smtp.*` to `config.yaml` (see [Registration and email](/admin/registration)).
- Single sign-on: [Sign in with Keycloak](./sso-keycloak).
- Backups: [Back up and restore](./backup-restore). Back up the `npm-data` and `npm-letsencrypt` volumes too, or set the proxy host up again after a restore.
- More servers or S3: see [Storage](/admin/storage).
