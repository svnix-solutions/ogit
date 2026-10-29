# Actions

oGit runs GitHub Actions workflows on your own runners. Put workflows in `.ogit/workflows/`, or in `.github/workflows/` when `.ogit/workflows/` has none, and they run with [act](https://github.com/nektos/act), in Docker. The [runner guide](/guides/run-actions) sets up a runner and a first workflow.

```yaml
# .ogit/workflows/ci.yaml
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: {go-version: "1.25"}
      - run: go test ./...
```

## Triggers

| Event | |
|---|---|
| `push` | with `branches`, `tags` and `paths` filters, and their `-ignore` forms |
| `pull_request` | `types`, base `branches`, `paths` |
| `workflow_dispatch` | started by writers, with inputs, from the Actions tab's **Run workflow** form |
| `schedule` | on the default branch, at most every 5 minutes |
| `issues`, `issue_comment`, `release`, `gollum`, `create`, `delete` | the default branch's workflows; `types:` filters the action |

Pushes made by workflows start no workflows.

### Pull requests

Pull request runs test `refs/pull/<n>/merge`, the pull request merged into its base (the head itself when they conflict), and post their checks on the head commit.

Runs from [forks](./forks) get no secrets and a read-only token. A first-time contributor's runs (someone with no merged pull request here) wait until a maintainer clicks **Approve and run**.

## Runners

Runners take queued runs from their scope: one repository, a group's repositories (sub-groups included), or every repository for runners shared by the admins. A runner takes a run when it has every `runs-on` label, and runs its jobs (`needs`, matrices) with act. Labels map to Docker images; by default `ubuntu-latest`, `ubuntu-24.04` and `ubuntu-22.04` are Node images with git.

Maintainers add runners in **Settings → Runners**, group owners on the group's page, and admins on the admin page.

## Secrets and variables

**Settings → Secrets** and **Variables** (maintainers), the same on a group's page (its owner) and the admin page:

- <span v-pre>`${{ secrets.NAME }}` and `${{ vars.NAME }}`</span> in workflows.
- The repository's override the group's (the nearest group first), which override the site's.
- Secrets are stored encrypted and never shown again. Runs from forks don't get them.

## `GITHUB_TOKEN`

Each job gets a token valid while its run runs, for its repository only. As on GitHub, it's <span v-pre>`${{ secrets.GITHUB_TOKEN }}`</span> (or <span v-pre>`${{ github.token }}`</span>), not an environment variable: pass it to a step with `env:` when a command needs it. Actions such as `actions/checkout` get it by themselves.

- it reads the repository (`actions/checkout` works as is);
- with `permissions: contents: write`, it pushes;
- with `statuses: write`, it posts commit statuses; with `packages: write`, it publishes [packages](./packages).

Without `permissions:` it only reads, and from forks it always only reads.

## Checks, artifacts and caches

- **Checks:** each job posts a commit status, `<workflow> / <job> (<event>)`, which [branch protection](./branch-protection) can require.
- **Artifacts:** use `ogit/upload-artifact@v4` and `ogit/download-artifact@v4`: the same inputs and outputs as `actions/upload-artifact@v4` and `download-artifact@v4`, which only work with github.com. They're stored with the repository (up to 2 GB each by default) and kept 90 days (`retention-days` can only shorten it). Anyone who can read the repository downloads them from the run's page; writers delete them.
- **Caches:** `actions/cache@v4` works as is. A run restores entries saved on its branch, then its pull request's base branch, then the default branch, and saves to its own branch only (never from forks). Each repository keeps up to 10 GB by default, dropping the least recently used entries, and entries unused for 7 days. The job's image needs GNU tar: the default Node images have it, Alpine's BusyBox tar doesn't work. Maintainers see and delete entries in **Settings → Caches**.

## Runs

The **Actions** tab (once the repository has workflows or runs) lists runs by workflow, branch, event and status. A run's page shows its jobs and follows the logs live, with **Cancel** and **Re-run** for writers. Pull requests list their runs next to their checks. A run whose runner stops reporting for 10 minutes fails.

## Badges

```md
![CI](https://git.example.com/me/my-project/actions/workflows/ci.yaml/badge.svg)
```

It shows the latest finished run of that workflow on the default branch (`?branch=` for another).

## API

| | |
|---|---|
| `POST /api/v1/repository/<owner>%2F<repo>/actions/runners/registration-token` | a runner registration token (maintainers); groups: `/api/v1/group/<name>/runners/registration-token`; shared: `/api/v1/admin/runners/registration-token` |
| `GET /api/v1/repository/<owner>%2F<repo>/actions/runs` | runs, with `?workflow=&branch=&event=&status=&pull=` |
| `GET /api/v1/repository/<owner>%2F<repo>/actions/runs/<n>`, `.../runs/<n>/logs?from=` | a run with its jobs, and its log (incremental) |
| `POST .../runs/<n>/cancel`, `.../rerun`, `.../approve` | writers; approve a waiting fork run (maintainers) |
| `POST /api/v1/repository/<owner>%2F<repo>/actions/workflows/<file>/dispatches` | start a `workflow_dispatch` run: `{"ref", "inputs"}` |
| `PUT\|DELETE /api/v1/repository/<owner>%2F<repo>/actions/secrets/<NAME>` | `{"value"}`; `.../variables/<NAME>` likewise, and the same under `/api/v1/group/<name>/actions/` and `/api/v1/admin/actions/` |

Another run's artifacts are read through GitHub's REST API: `/api/v3/repos/<owner>/<repo>/actions/runs/<id>/artifacts`, used by `download-artifact` with `run-id` and `github-token`.
