# Workspaces

::: warning Not usable yet
This release only has workspace records and their API. Nothing runs them until the workspace agent ships, so a started workspace stays in `starting`.
:::

A workspace is a user's development container for one branch of a repository. Workspaces are off until you set `workspaces.domain`.

## Configuration

| Key | Default | |
|---|---|---|
| `workspaces.domain` | empty | Workspaces are served at `<id>.<domain>`, which needs wildcard DNS and TLS. Empty turns them off. It must differ from `http.public_url`'s host, so workspace code never shares an origin with oGit's sessions. |
| `workspaces.cpus` | `2` | CPUs per workspace |
| `workspaces.memory_gb` | `4` | Memory per workspace |
| `workspaces.disk_gb` | `10` | Disk per workspace |
| `workspaces.idle_timeout` | `30m` | Stopped after this long idle |
| `workspaces.retention` | `336h` | Deleted after this long unused (14 days) |
| `workspaces.max_running` | `1` | Active workspaces per user |

Only `domain` and `max_running` take effect so far; the other limits apply once the agent ships.

## Rules

- Developers and up create and start workspaces, on repositories that take pushes. Readers fork first.
- A workspace is private to its user, whatever other people's roles.
- Users can always stop and delete their own workspaces, even after losing access to the repository, or after workspaces are turned off.
- Only stopped workspaces can be deleted.
- Deleting a repository or a user deletes their workspaces.

## API

| | |
|---|---|
| `POST /api/v1/repository/<owner>%2F<repo>/workspaces` | create one, stopped: `{"branch"}` (the default branch when empty) |
| `GET /api/v1/repository/<owner>%2F<repo>/workspaces` | your workspaces on the repository |
| `GET /api/v1/user/workspaces`, `GET .../<id>` | yours; `repository` is empty if you can't read it any more |
| `POST /api/v1/user/workspaces/<id>/start`, `.../stop` | start and stop; both are fine to repeat |
| `DELETE /api/v1/user/workspaces/<id>` | delete a stopped workspace |

Errors: `WORKSPACES_DISABLED` (503), `WORKSPACE_LIMIT`, `WORKSPACE_ACTIVE` and `WORKSPACE_EXISTS` (409), `WORKSPACE_BRANCH_NOT_FOUND` (400), `WORKSPACE_NOT_FOUND` (404).
