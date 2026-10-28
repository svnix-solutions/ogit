# Site administration

Site admins manage users, repositories, groups, sign-in providers, runners and site settings, on the admin page (`/admin`) or through `/api/v1/admin`. Admin actions need [two-factor authentication](./two-factor).

## From the server

These commands write to the database directly, like `migration`, and need no session. They are the bootstrap and recovery path:

```bash
./ogit admin promote   -c config.yaml --email you@example.com   # or --username
./ogit admin demote    -c config.yaml --username someone
./ogit admin reset-2fa -c config.yaml --username someone        # lost device and recovery codes
```

## The admin API

`/documentation` lists every endpoint with its fields.

| Endpoint | |
|---|---|
| `GET /api/v1/admin/users?q=&limit=&offset=` | list and search users |
| `POST /api/v1/admin/users` | create a verified account with an initial password (also when registration is off) |
| `PATCH /api/v1/admin/users/<username>` | `{"active": false}` disables the account and ends its sessions; also `"email_verified": true`, `"role": "admin"` or `"user"` |
| `DELETE /api/v1/admin/users/<username>` | delete a user and their sessions, tokens and memberships |
| `POST /api/v1/admin/users/<username>/reset-2fa` | turn off a user's 2FA (they're emailed and can set it up again) |
| `GET /api/v1/admin/repositories` | list every repository |
| `POST /api/v1/admin/repositories/<owner>%2F<slug>/owner` | make a user owner, e.g. when a repository's owners are gone |
| `POST /api/v1/admin/repositories/<owner>%2F<slug>/transfer` | `{"namespace": "..."}` moves it to any user's or group's namespace |
| `DELETE /api/v1/admin/repositories/<owner>%2F<slug>?confirm=<slug>` | delete a repository and its git data |
| `POST /api/v1/admin/groups/<name>/transfer` | make a user the group owner |
| `PATCH /api/v1/admin/settings` | site settings: `require_two_factor`, `sso_only` |

## Rules

- Admins have no implicit access to repository content. To get access, an admin makes themselves owner, which shows in the member list.
- A user is not deleted while they're the only owner of a repository, have repositories in their namespace, or own a group. The error lists them, so the admin can hand them over or move them first.
- The API never removes the last active admin; the CLI can always promote one.
- Disabling a user ends their sessions at once; their tokens and SSH keys stop working.
- Admin actions are written to the [audit log](./monitoring#audit-log).
