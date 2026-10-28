# Migrate a repository from GitHub

This guide moves a GitHub repository to oGit with its history, issues, pull requests, labels, milestones and releases. The same steps work for GitLab and Gitea or Forgejo.

## 1. Make a GitHub token

On GitHub, **Settings → Developer settings → Personal access tokens → Fine-grained tokens**, make a token for the repository with read access to **Contents**, **Issues**, **Pull requests** and **Metadata**. For a public repository, a token is still worth it: GitHub's API allows far more requests with one.

## 2. Prepare the people

The import matches GitHub users to accounts here by **user name** (or public email). Ask your team to create their oGit accounts with the same user names first. Anything by someone without an account here appears under your name, with a line saying who wrote it originally.

## 3. Import

In oGit, click **New repository**, then **Import from a URL**:

| Field | Value |
|---|---|
| Remote URL | `https://github.com/owner/repo.git` |
| Username / Password or token | your GitHub user name and the token |
| Keep it as a mirror | off: you're moving, not mirroring |
| Fetch Git LFS objects too | on, if the repository uses LFS |
| Also import issues, pull requests, labels, milestones and releases | on, **From** GitHub |
| Owner and Name | where it goes here |

The token you entered is also used for the API, unless you give a separate **API token**. For GitHub Enterprise, the **API URL** is worked out from the remote URL; set it if that fails.

The code comes first; the rest follows in the background. The repository's **Settings → Import of issues and pull requests** shows the progress and any warnings. GitHub's rate limits can pause it for a while; it resumes by itself.

## 4. Check the result

- **Numbers** are kept: issue #42 on GitHub is #42 here.
- **Open pull requests** are open here too. Those whose branch was imported can be merged; those from forks show `owner:branch` and stay read-only.
- **Reviews** by people without an account here became comments.
- **Releases** have their files, up to your server's file limit.

## 5. Switch over

1. Point your clones at oGit:

   ```bash
   git remote set-url origin https://git.example.com/owner/repo.git
   ```

2. Move CI to [Actions](/user/actions) runners. Workflows in `.github/workflows/` work as they are, and `.ogit/workflows/` takes precedence if you'd rather keep both.
3. Archive the GitHub repository, or keep it updated with a [push mirror](/user/mirroring#push-mirrors) while people move.

To keep using GitHub for a while and only follow it from oGit, import it with **Keep it as a mirror** on instead. Note that a mirror is read-only here.
