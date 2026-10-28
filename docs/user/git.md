# Git over HTTP and SSH

Every repository has a path, `<owner>/<repo>`: its owner is a user or a group, and its name is made URL-safe (`My Project` created by `me` becomes `me/my-project`). Repositories in sub-groups have longer paths (`org/team/app`). The repository's page shows its clone URLs.

## Over HTTPS

```bash
git clone https://git.example.com/me/my-project.git
```

Public repositories clone without credentials. For anything else, git asks for a user name and a password: use an [access token](./account#access-tokens), with the token's **name** as the user name and the **token** as the password. A credential helper (`git config --global credential.helper osxkeychain`, `manager`, `store`…) remembers it.

| You | Clone and fetch | Push |
|---|---|---|
| Anonymous | public repositories | never |
| A token with `read:repository` | public repositories, and private ones you can read | no |
| A token with `write:repository` | the same | where you're developer or higher |

Private repositories you can't read answer "not found", like a missing one.

## Over SSH

If your server has SSH turned on, add your public key in **Settings → SSH keys**, then:

```bash
git clone ssh://git@git.example.com:2222/me/my-project.git
ssh -T -p 2222 git@git.example.com    # "Hi <you>! You've successfully authenticated…"
```

Everyone connects as `git`: your key says who you are. The same access rules as HTTPS apply. The port is set by your administrator (2222 by default).

To skip the port in URLs, add a host entry to `~/.ssh/config`:

```txt
Host git.example.com
  Port 2222
```

## Pushing

- A push larger than the server's limit (2 GB by default) is refused, with "push is larger than the limit". A refused or interrupted push leaves nothing behind.
- Protected branches may refuse a push. Git shows the reason for each branch that was refused, and the other branches still go through. See [Branch protection](./branch-protection).
- An archived repository or a mirror refuses every push.

Large binary files belong in [Git LFS](./lfs).

## Raw files and archives

```bash
curl -O https://git.example.com/me/my-project/raw/main/docs/guide.md      # a file at a branch, tag or commit
curl -O https://git.example.com/me/my-project/archive/v1.0.tar.gz         # or .zip
curl -u laptop:$TOKEN -O https://git.example.com/me/private-project/raw/main/x   # private: a token, as for git
```

Branch names may contain slashes (`raw/feature/x/README.md`). Files are served as plain text or as downloads, never as web pages, so a file in a repository can't run scripts on the server's site. The **Branches** page has zip and tar.gz downloads for every branch.

## Deploy keys

A deploy key is an SSH key that belongs to one repository rather than a person: for a server or a CI job that clones (or, if allowed, pushes) just that repository. Maintainers add them under the repository's **Settings → Deploy keys**. They're read-only unless marked writable, and a key can't be both a deploy key and someone's account key.
