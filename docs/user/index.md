# Using oGit

These pages are for people who use an oGit server: working with repositories, reviewing code, tracking work, publishing releases and packages, and running CI. The [guides](/guides/) walk through common tasks step by step; running a server is under [Administration](/admin/).

## Where things are

| Page | |
|---|---|
| `/explore` | public repositories, no account needed |
| `/<owner>` | a user's or group's repositories, sub-groups and packages |
| `/<owner>/<repo>` | a repository: **Code**, **Issues**, **Pull requests**, **Commits**, **Branches**, **Compare**, **Wiki**, **Releases**, **Actions** and **Packages** (when used), **Settings** (maintainers and up) |
| `/new` | a new repository, empty or imported from a URL |
| `/groups` | your groups, and new ones |
| `/notifications` | your inbox (the bell in the header shows the unread count) |
| `/settings` | your account: password, two-factor authentication, sign-in providers, access tokens, SSH keys, notification emails |

Everything the web UI does goes through the REST API, documented at `/documentation` on your server. The pages here show API calls next to the UI where they help, with `$SESSION` a session token from `POST /api/v1/auth/login`.

## Roles

Access to a repository comes from your role there, given directly or through a group:

| Role | Can |
|---|---|
| **reporter** | read and clone; open issues, comment, review |
| **developer** | also push, open pull requests, edit and close issues, set labels, assignees and milestones, publish releases, edit the wiki |
| **maintainer** | also merge pull requests, manage labels, milestones and members (up to developer), webhooks, deploy keys, mirrors, Actions settings |
| **owner** | everything: rename, visibility, archive, transfer, delete, branch protection |

Public repositories can be read by anyone, signed in or not. A private repository you can't read looks exactly like one that doesn't exist.

## Topics

- [Your account](./account): sign-in, two-factor authentication, tokens and keys
- [Git over HTTP and SSH](./git): cloning, pushing, raw files and archives
- [Repositories](./repositories) and [Groups](./groups)
- [Pull requests](./pull-requests) and [Branch protection](./branch-protection)
- [Issues](./issues), [Wikis](./wikis) and [Notifications](./notifications)
- [Releases](./releases), [Packages](./packages) and [Git LFS](./lfs)
- [Forks](./forks) and [Mirroring and imports](./mirroring)
- [Actions](./actions): CI workflows
