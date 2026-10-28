# Run oGit with Docker and PostgreSQL

This guide sets up oGit on one host with Docker Compose: PostgreSQL for the database, a volume for the repositories, git over SSH, and Caddy for TLS. Replace `git.example.com` with your host name, and point its DNS at the host.

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
  trusted_proxies: ["172.16.0.0/12"]   # Docker's networks, where Caddy is

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
    ports:
      - "2222:2222"   # git over SSH

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy:/data

volumes:
  db:
  data:
  caddy:
```

`Caddyfile`:

```txt
git.example.com {
    reverse_proxy ogit:8080 {
        flush_interval -1
        transport http {
            read_timeout 10m
            write_timeout 10m
        }
    }
}
```

## 4. Start it

```bash
docker compose up -d
docker compose logs -f ogit    # the migrations, then the server starting
curl https://git.example.com/api/health
```

The container runs the migrations on every start.

## 5. Create the first admin

Register at `https://git.example.com/register`, then:

```bash
docker compose exec ogit ./app admin promote -c /config/config.yaml --email you@example.com
```

Sign in again, set up two-factor authentication (admin actions need it), and open `/admin`.

## Next steps

- Outgoing mail: add `smtp.*` to `config.yaml` (see [Registration and email](/admin/registration)).
- Single sign-on: [Sign in with Keycloak](./sso-keycloak).
- Backups: [Back up and restore](./backup-restore).
- More servers or S3: see [Storage](/admin/storage).
