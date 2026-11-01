# Workspaces

::: warning Not usable yet
Agents now run workspaces, but nothing reaches them yet: the browser proxy and the image with the network rules ship in later releases. Until then, a workspace's code editor can't be opened and its code has unfiltered network access, so don't turn this on outside a test environment.
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
| `workspaces.max_total` | `5` | Workspaces per user, stopped ones included (`0`: no limit) |
| `workspaces.pids` | `1024` | Processes per workspace |
| `workspaces.image` | code-server, by digest | Image of workspaces, which must be pinned as `name@sha256:...` |

`idle_timeout` has no effect yet, and `retention` only applies to workspaces whose user lost access: unused workspaces aren't deleted yet.

## Agents

`ogit workspace-agent` runs workspaces on a Docker host, which can be any machine that reaches the server. The server never touches Docker, so code in a workspace stays away from it.

```bash
curl -X POST $OGIT/api/v1/admin/workspace-agents/registration-token -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"reusable": true, "name": "fleet"}'
./ogit workspace-agent register --url $OGIT --token ogitwreg_...
./ogit workspace-agent run
```

`register` writes the agent's token to `.workspace-agent`. A registration token works once, within the hour, unless it's reusable.

- `--git-url`: where workspaces reach the server, if not the registered URL, such as `http://ogit:8080` with `--network`.
- `--network`: the Docker network of workspaces.
- `--max-workspaces`: how many it runs at a time (default 10).
- `--docker-host`: the Docker socket.

Each workspace is a container with no Linux capabilities, no privilege escalation, a read-only root, a non-root user, the limits above, and no published port. Its files are a Docker volume on that agent, so a stopped workspace can only start again there, and starting it is refused while that agent is offline.

**Disk:** Docker can't limit a volume's size on its usual storage drivers. The agent checks volumes every 5 minutes and stops a workspace over `disk_gb`, so a workspace can run past its limit for that long. Leave room on the host.

An agent unseen for 5 minutes loses its active workspaces, which stop with the reason. If it comes back, it removes their containers and keeps their volumes. Deleting an agent makes it exit and forgets its workspaces, which start afresh elsewhere; remove what's left with `docker volume ls --filter label=ogit.workspace`.

**Git access:** each workspace has a token for its repository only, acting as its user with their current role. It lives on a tmpfs in the container, is cleared the moment the workspace stops, and expires within an hour if the agent stops reporting.

## Rules

- Developers and up create and start workspaces, on repositories that take pushes. Readers fork first.
- A workspace is private to its user, whatever other people's roles.
- Users can always stop and delete their own workspaces, even after losing access to the repository, or after workspaces are turned off.
- Only stopped workspaces can be deleted, by their user. Admins can stop and delete any, which is logged.
- Deleting a repository or a user deletes their workspaces.
- A workspace whose user is disabled, or can no longer read the repository, stops within a minute and its git token goes. Its files stay for `workspaces.retention`, then it's deleted, including anything not pushed. If the user gets access back before that, it stays and they can start it again.

## API

| | |
|---|---|
| `POST /api/v1/repository/<owner>%2F<repo>/workspaces` | create one, stopped: `{"branch"}` (the default branch when empty) |
| `GET /api/v1/repository/<owner>%2F<repo>/workspaces` | your workspaces on the repository |
| `GET /api/v1/user/workspaces`, `GET .../<id>` | yours; `repository` is empty if you can't read it any more |
| `POST /api/v1/user/workspaces/<id>/start`, `.../stop` | start and stop; both are fine to repeat |
| `DELETE /api/v1/user/workspaces/<id>` | delete a stopped workspace |

| `GET /api/v1/admin/workspaces` | every workspace (admins) |
| `POST /api/v1/admin/workspaces/<id>/stop`, `DELETE /api/v1/admin/workspaces/<id>` | stop or delete anyone's, running ones too (admins) |
| `GET /api/v1/admin/workspace-agents`, `DELETE .../<id>` | agents |
| `POST /api/v1/admin/workspace-agents/registration-token` | one-time, or `{"reusable": true, "name", "expires_at"}` (`expires_at` optional) |
| `GET .../registration-tokens`, `DELETE .../registration-tokens/<id>` | reusable tokens, and revoking one (the agents stay) |

A workspace that stopped by itself says why in `failure`.

Errors: `WORKSPACES_DISABLED` and `WORKSPACE_AGENT_OFFLINE` (503), `WORKSPACE_LIMIT`, `WORKSPACE_TOTAL`, `WORKSPACE_ACTIVE` and `WORKSPACE_EXISTS` (409), `WORKSPACE_BRANCH_NOT_FOUND` and `INVALID_AGENT_TOKEN` (400), `WORKSPACE_NOT_FOUND`, `WORKSPACE_AGENT_NOT_FOUND` and `AGENT_TOKEN_NOT_FOUND` (404).
