# Tokens

Tools authenticate with tokens, never with passwords. Tokens look like `ogit_` followed by 64 hex characters. The server stores only a hash, so a token is shown once, when it's made.

## Personal access tokens

A personal access token acts as its owner, limited to its scopes. Users make them in **Settings → Access tokens** ([details](/user/account#access-tokens)).

| Scope | Allows |
|---|---|
| `read:repository` | clone, fetch, read the repositories the user can read |
| `write:repository` | push where the user is developer or higher |
| `read:packages` / `write:packages` | pull and publish packages |
| `write:status` | post commit statuses, without git access |

Use it as:

- **git over HTTPS:** user name = the token's name, password = the token;
- **HTTP:** `Authorization: token <token>`, `Authorization: Bearer <token>`, or Basic with the token as the password.

A token can expire. Bad tokens count as failed logins for the caller's IP, so a misconfigured job gets rate-limited rather than guessing forever.

## Repository tokens

A repository token belongs to one repository, not a person, so CI needs no personal account. Maintainers and owners make them in the repository's **Settings → Access tokens**.

```bash
curl -X POST https://git.example.com/api/v1/repository/me%2Fmy-project/tokens -H "Authorization: Bearer $SESSION" \
  -H 'Content-Type: application/json' -d '{"name": "ci", "scopes": ["read:repository", "write:status"]}'
git clone https://ci:$TOKEN@git.example.com/me/my-project.git
curl https://git.example.com/api/v1/repository/me%2Fmy-project/issues -H "Authorization: token $TOKEN"
```

| Scope | Allows |
|---|---|
| `read:repository` | clone, fetch, and read this repository's API: files, pull requests, issues, milestones, statuses |
| `write:repository` | push, under branch protection (a token is never on a rule's list of who may push), and publish releases |
| `write:status` | post commit statuses |
| `read:packages` / `write:packages` | pull and publish the packages linked to this repository, and create new ones linked to it |

- A repository token reaches its own repository only.
- It never acts as a person: no comments, reviews or other writes in someone's name.
- Expiry is optional, and the token list shows each token's last use.

## Actions job tokens

Each [Actions](/user/actions) job gets a token, <span v-pre>`${{ secrets.GITHUB_TOKEN }}`</span>, valid while its run runs, for its repository only. It reads the repository; with `permissions:` it can push (`contents: write`), post statuses (`statuses: write`) and publish packages (`packages: write`). It works wherever a repository token does.

## Deploy keys

For SSH, the counterpart of a repository token is a [deploy key](/user/git#deploy-keys): an SSH key bound to one repository, read-only unless marked writable.
