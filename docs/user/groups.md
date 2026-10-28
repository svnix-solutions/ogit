# Groups

Every repository belongs to a user or a group: its **namespace**, the first part of its path. Usernames and group names share one set of names (lowercase letters, digits, `.`, `_`, `-`; not ending in `.git`; a few words such as `admin`, `api`, `explore` and `settings` are reserved), so `/<name>` always means one thing.

## Groups

Anyone can create a group on `/groups`, and becomes its owner. A group has:

- one **owner**: manages maintainers, transfers ownership (**Transfer ownership**) and deletes the group;
- **maintainers**: add and remove members, create repositories and sub-groups in the group;
- **members**.

Groups are visible to their members only. On a group's repositories, its owner is owner, maintainers are maintainers and members are developers. Repositories created in a group's namespace have no separate owner: the group owner is theirs.

A group with repositories or packages in its namespace can't be deleted; move or delete them first. The same goes for users.

```bash
curl -X POST https://git.example.com/api/v1/group -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "backend"}'
curl -X POST https://git.example.com/api/v1/group/backend/members -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"username": "bob", "role": "member"}'
```

A group can also be given a role on a repository outside its namespace (at most maintainer), from that repository's **Settings → Members**.

## Sub-groups

A group's maintainers and owner create sub-groups in it (**Sub-groups** on the group's page), up to 5 levels deep. Their repositories live at `org/team/app`, over HTTP and SSH alike.

- **Membership is inherited:** members of a group are members of every sub-group below it, with the same role. A sub-group can add its own members, or raise an inherited member's role, never lower it.
- **A path is either a sub-group or a repository:** `org/team` can't be both.
- **Settings are inherited too:**
  - a group that requires two-factor authentication requires it in every sub-group;
  - group Actions runners serve the sub-groups' repositories, and group secrets and variables reach them (the nearest group wins, and the repository's own win over all);
  - group webhooks fire for every repository in the group and below.
- In the API, a group's path is URL-encoded where one segment is expected: `/api/v1/group/org%2Fteam`.

### Moving and renaming

A group's owner renames a sub-group, or moves a group under another one they maintain (`"parent": ""` makes it top-level again). Everything below moves along. Every former path, of groups, repositories and git URLs, redirects until something else takes it. Top-level groups can't be renamed, because they share names with users, but they can move under another group.

```bash
curl -X POST https://git.example.com/api/v1/group/org%2Fteam/move -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "platform"}'
```

## Requiring two-factor authentication

A group's owner can require two-factor authentication. Members without it keep their membership, but get no access to the group's repositories through the group, and can't create repositories in it, until they set it up. Owners and maintainers see who lacks it.
