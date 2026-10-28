# Rate limits

Rate limits are on by default. Each rule is `N/window`; `"0"` turns a rule off. Going over a rule answers 429 with `Retry-After`.

```yaml
ratelimit:
  enabled: true
  store: memory          # memory (per server) or redis (shared by every server)
  redis_url: ""          # e.g. redis://cache:6379/1
  login_ip: 10/15m       # failed logins, password checks and git token checks per IP
  login_account: 50/15m  # failed logins per account, from any IP
  register_ip: 5/1h      # registrations and verification emails per IP
  api_user: 600/1m       # API requests per signed-in user
  api_anonymous: 120/1m  # API requests per IP without a session
  git_user: 300/1m       # git operations, raw files and archives per token, user or SSH key
  git_anonymous: 60/1m   # the same per IP without credentials
```

- **Password and token guessing:** only failures count. Once over the limit, even the right password gets 429 until the window ends.
- **API:** the `RateLimit-*` headers show what's left.
- **Git:** HTTP, SSH, raw files and archives.

## Several servers

Counters live in memory, so each server counts on its own. With `store: redis`, all servers share them. If Redis is down, requests are allowed and the error is logged.

## Behind a proxy

List the proxies in `http.trusted_proxies` (see [Behind a reverse proxy](./reverse-proxy)). Otherwise every request seems to come from the proxy, and they all share one budget.
