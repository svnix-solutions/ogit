# Integrating with oGit

These pages are for people connecting tools to oGit: CI systems, bots, scripts, release tools. oGit speaks its own REST API, and GitHub's where tools expect it.

| | |
|---|---|
| [The REST API](#the-rest-api) | everything the web UI does |
| [Tokens](./tokens) | personal access tokens, repository tokens, Actions job tokens |
| [Webhooks](./webhooks) | GitHub-compatible events, signed and retried |
| [Commit statuses](./statuses) | CI results on commits, required checks |
| [GitHub-compatible API](./github-api) | for tools built for GitHub: statuses, releases, archives, artifacts |

## The REST API

The API is under `/api/v1`. Every server documents it at **`/documentation`** (interactive), with the OpenAPI document at `/api-spec.json`, so you can generate clients from it.

### Authentication

| Credential | How | For |
|---|---|---|
| A session token | `POST /api/v1/auth/login {"email", "password"}` returns `token`; send `Authorization: Bearer <token>` | scripts acting as a person |
| A personal access token | `Authorization: token <token>`, `Bearer <token>`, or Basic with the token as the password | git, packages, commit statuses, releases, the GitHub-compatible API |
| A repository token | the same headers | CI for one repository |

- Sessions end with `POST /api/v1/auth/logout` or after `session.expiration_minutes` (30 days by default). With two-factor authentication, login answers `{"two_factor_required": true, "challenge": "…"}` instead: complete it with `POST /api/v1/auth/login/2fa {"challenge", "code"}`.
- Some actions need a recent confirmation ("sudo"). They answer 403 `SUDO_REQUIRED` otherwise: confirm with `POST /api/v1/auth/sudo {"code": …}` (or `{"password": …}` without 2FA), then retry.
- An unauthenticated request to a route that needs a session gets **400** "authentication required", not 401.

### Repositories in URLs

A repository is its id, or its path with the slash URL-encoded: `/api/v1/repository/me%2Fmy-project`. A sub-group's repository encodes every slash: `org%2Fteam%2Fapp`. Renamed and moved repositories answer at their old paths too.

### Errors

Errors are JSON, with an HTTP status, a stable `type` to test for, and a message for people:

```json
{"code": 409, "type": "WIKI_CONFLICT", "details": "the page changed since you started editing it: reload it and apply your changes again"}
```

A private repository you can't read answers 404, exactly like a missing one.

### Rate limits

Requests are counted per signed-in user (600 a minute by default) or per IP without a session (120). The `RateLimit-*` headers show what's left. Over the limit, you get 429 with `Retry-After`. Failed logins and bad tokens have their own, stricter limits.

### The web UI's sessions

The web UI signs in with `POST /api/v1/auth/session`, which keeps the session in an HttpOnly cookie (`ogit_session`) that page scripts can't read. Writes authenticated by that cookie must also send the `ogit_csrf` cookie's value in an `X-CSRF-Token` header (403 `CSRF_FAILED` otherwise). API clients using bearer tokens need neither.
