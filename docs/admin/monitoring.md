# Logging and monitoring

## Health check

```bash
curl http://localhost:8080/api/health
# {"status":"ok","service":"ogit","version":"v1.2.3"}
```

It answers as soon as the server listens, without a session. Use it for container and load balancer health checks. It doesn't check the database or the storage.

## Logs

oGit logs JSON lines to standard output, one object per event, with `level`, `time`, `message` and a `component`.

```yaml
logger:
  level: info     # debug, info, warn, error
  pretty: false   # human-readable output instead of JSON (for a terminal)
http:
  logs: false     # an access log line per request
```

Tokens, session tokens and passwords are never logged, and SQL logs leave out bound values.

## Audit log

Security-relevant actions are logged with `"component":"audit"`. Filter on it to keep them apart:

```bash
docker logs ogit 2>&1 | jq -c 'select(.component == "audit")'
```

They include:

- admin actions on users, repositories, groups and site settings;
- sign-ins through providers, provider links and provider changes;
- branch protection and protected tag bypasses by owners;
- two-factor authentication and security keys turned on, off or reset;
- group moves and group 2FA requirements, mirrors and imports, and package deletions;
- Actions runners registered and deleted, and secrets set.
