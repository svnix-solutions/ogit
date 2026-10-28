# Behind a reverse proxy

oGit serves plain HTTP. Put a reverse proxy in front of it for TLS, and tell oGit about it:

```yaml
http:
  public_url: https://git.example.com
  trusted_proxies: ["10.0.0.0/8"]   # the proxies' addresses (IPs or CIDRs)
```

- **`public_url`** is the address users see. With an `https://` URL, the web UI's session cookies are `Secure`.
- **`trusted_proxies`**: `X-Forwarded-For` and `X-Forwarded-Proto` are only believed from these addresses, and `X-Forwarded-For` is read from the right, so a client can't choose its own address. Without it, every request seems to come from the proxy, and they all share one [rate limit](./rate-limits) budget.

## What the proxy must allow

| | Why |
|---|---|
| Large request bodies | Pushes (up to `git.max_push_size`, 2 GB), LFS objects, release assets and packages are streamed. Don't cap bodies below those limits, and don't buffer them. |
| Long requests | Big clones and pushes take minutes; Actions runners long-poll `/api/actions/runner/fetch`. Use read and send timeouts of several minutes. |
| `X-Forwarded-For`, `X-Forwarded-Proto` | So oGit sees the client's address and that the request came over HTTPS. |

## nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name git.example.com;
    # ssl_certificate ...;

    client_max_body_size 0;          # oGit enforces its own limits
    proxy_request_buffering off;     # stream pushes and uploads
    proxy_buffering off;
    proxy_read_timeout 600s;
    proxy_send_timeout 600s;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Nginx Proxy Manager

Create a proxy host that forwards to oGit (`http`, port 8080), request a certificate on the **SSL** tab with **Force SSL**, and put the nginx settings above (body size, buffering, timeouts) in the **Advanced** tab's custom configuration. The [Docker guide](/guides/docker-postgres#_5-set-up-the-proxy-host) goes through it.

## Caddy

```txt
git.example.com {
    reverse_proxy 127.0.0.1:8080 {
        flush_interval -1
        transport http {
            read_timeout 10m
            write_timeout 10m
        }
    }
}
```

Caddy sets the forwarded headers and doesn't limit body sizes by default.

## SSH

Git over SSH is a separate port (`ssh.port`, 2222), not HTTP: forward it as TCP. That can be a port published straight to the server, a load balancer's TCP listener, an nginx `stream` block, or a Nginx Proxy Manager **stream** (see the [Docker guide](/guides/docker-postgres#_6-forward-ssh-with-a-stream)). Caddy needs the third-party `caddy-l4` plugin for it. A TCP proxy hides the client's address from oGit; SSH rate limits count per key, so they still work. Clone URLs then look like `ssh://git@git.example.com:2222/<owner>/<repo>.git`. Keep `ssh.host_key_path` on persistent storage: when the host key changes, clients warn about a possible attack.
