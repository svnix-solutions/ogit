# Mirroring and imports

oGit can copy a repository from anywhere once (an **import**), keep following it (a **pull mirror**), bring its issues and pull requests along from GitHub, GitLab or Gitea, and push your repositories on to other servers (**push mirrors**).

## Importing a repository

On **New repository**, choose **Import from a URL**:

- **The remote:** `https://` (with a user name and a password or token if it's private), or `ssh://` / `git@host:path`. For SSH, oGit makes a key for the repository and shows its public key, to add on the remote (as a deploy key, for instance). The remote's host key is remembered on first contact. Credentials and keys are stored encrypted and never shown again.
- **Import or mirror:** an import copies the branches and tags once, and then it's a normal repository. A mirror keeps following the remote (below).
- **Git LFS:** also fetch the LFS objects the branches and tags point to.
- **Also import issues, pull requests, labels, milestones and releases**, for GitHub, GitLab and Gitea/Forgejo remotes (below).

The [migration guide](/guides/migrate-from-github) goes through it.

## Pull mirrors

A mirror follows its remote, every 8 hours by default:

- Branches and tags deleted upstream are deleted here too.
- It refuses pushes and merges. To work in it, an owner stops mirroring (it becomes a normal repository).
- Maintainers see its status and last error in **Settings → Mirror**, change the interval or credentials, and **Sync now**. Failed syncs are retried, waiting longer each time.

Your server may refuse remotes on private networks; your administrator can allow some.

## Issues, pull requests and releases

From GitHub (and GitHub Enterprise), GitLab or Gitea/Forgejo, an import can bring along:

- labels, milestones, and issues and pull requests with their comments, reviews, assignees and dates (inline review comments become comments that quote the file and line);
- releases with their files (GitLab's release links stay links in the notes).

It runs once, in the background, after the code. **Settings → Import of issues and pull requests** shows its progress, warnings and errors.

- **Numbers are kept.** Issues opened here in the meantime come after them. GitLab numbers merge requests apart from issues, so its merge requests come after the issues here (each says which `!n` it was).
- **People** are matched to accounts here by user name (or public email). Anything by someone else appears under your name, with a line "Originally by @login on GitHub", and their reviews become comments (an approval never counts as yours).
- **Open pull requests stay open:** mergeable when their branch was imported, read-only with their recorded changes otherwise (pull requests from forks show `owner:branch`).
- **Interruptions:** rate limits pause the import until the forge allows more; failures retry, then wait for **Resume**. Nothing is imported twice. No notifications, webhooks or Actions runs are sent for imported items.

## Push mirrors

**Settings → Push mirrors** (maintainers) pushes the repository on to up to 10 other remotes:

- **When:** within seconds of every push, merge, release tag, fork sync and pull mirror sync, so a repository can relay another, and every 8 hours by default. Pushing never waits for them.
- **Mode:** `mirror` (the default) force-pushes, and deletes the remote's branches and tags that this repository doesn't have. `branches` never forces or deletes: it pushes what it can, and reports the branches that diverged and the tags that moved.
- **Credentials** work like a pull mirror's. An SSH key needs write access on the remote, e.g. a deploy key allowed to push.
- **Git LFS:** optionally upload the LFS objects first.
- The wiki is pushed to the remote's wiki too, when it exists there.

## API

| | |
|---|---|
| `POST /api/v1/repository/import` | `name`, `url`, and optionally `namespace`, `username`, `password`, `mirror`, `lfs`, `interval_seconds`, `forge` (`github`, `gitlab`, `gitea`), `api_token`, `api_url` |
| `GET\|PATCH\|DELETE /api/v1/repository/<owner>%2F<repo>/mirror`, `POST /api/v1/repository/<owner>%2F<repo>/mirror/sync` | a pull mirror (`DELETE` stops mirroring, owners only) |
| `GET\|DELETE /api/v1/repository/<owner>%2F<repo>/import`, `POST /api/v1/repository/<owner>%2F<repo>/import/retry` | the issues and pull requests import |
| `GET\|POST /api/v1/repository/<owner>%2F<repo>/push-mirrors`, `PATCH\|DELETE .../push-mirrors/<id>`, `POST .../push-mirrors/<id>/sync` | push mirrors: `url`, `username`, `password`, `mode`, `lfs`, `interval_seconds` |
