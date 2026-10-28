# Branch protection

Owners protect branches in the repository's **Settings → Branch protection**, which offers to protect the default branch, or through the API. Developers and up can read the rules.

## Rules

A rule matches branches by name or pattern: `main`, `release/*`, `hotfix/**`. `*` stays within a path segment, and `**` crosses them. When several rules match a branch, the most specific one applies.

- **Always:** a protected branch refuses force-pushes and deletion, unless the rule allows them (`allow_force_push`, `allow_deletion`).
- **Require a pull request:** no direct pushes. The branch only moves by merging pull requests. Pushing the branch into existence is still allowed.
- **Approvals:** how many a pull request into the branch needs (the repository's setting when unset), and whether approvals of older commits count (`dismiss_stale_approvals`: only approvals of the current head do).
- **Required checks** (`required_contexts`): commit status contexts that must pass before merging, such as `ci/build` or an [Actions](./actions) job (`<workflow> / <job> (<event>)`).
- **Who may push and merge:** when users or groups are listed, only they may push to the branch or merge into it. They still need write access. Deploy keys and repository tokens are never on the list.
- **Owners may bypass:** with `owners_bypass`, repository owners can break the rule. Every bypass is written to the audit log.

```bash
curl -X POST https://git.example.com/api/v1/repository/me%2Fmy-project/branch-protection -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' \
  -d '{"pattern": "main", "require_pull_request": true, "required_approvals": 2, "dismiss_stale_approvals": true, "allowed_groups": ["release-team"]}'
```

## What people see

- A push that breaks a rule is refused **for that branch only**, and git shows why; other branches in the same push go through.
- A pull request's merge box says why you can't merge it, and which rule protects the branch.
- A protected source branch is kept after a merge, even if "delete branch" was ticked.

## Protected tags

**Settings → Protected tags** protects tags the same way, by name or pattern (`v*`):

- Only maintainers and up, and the users and groups listed, may create a matching tag, by pushing it or by publishing a [release](./releases). Deploy keys and repository tokens never may, but they can still publish a release for an existing protected tag.
- Nobody may move or delete a protected tag, except owners, and every such bypass is written to the audit log.

```bash
curl -X POST https://git.example.com/api/v1/repository/me%2Fmy-project/protected-tags -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"pattern": "v*", "allowed_users": ["release-bot"]}'
```
