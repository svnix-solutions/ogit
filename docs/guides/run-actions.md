# Run Actions on your own runner

Actions workflows run on runners you host: any Linux machine with Docker. This guide registers a runner for one repository and runs a first workflow. (The [Docker Compose showcase](./try-it) comes with a runner already registered for its demo repository.)

## 1. Get the runner

The runner is the `ogit` binary itself, as `ogit runner`. On the runner machine, build it from source, or copy it out of the oGit image:

```bash
docker create --name ogit-tmp ghcr.io/svnix-solutions/ogit:<tag>
docker cp ogit-tmp:/app ./ogit && docker rm ogit-tmp
./ogit runner --help
```

The machine needs Docker running, and to reach your oGit server's `http.public_url`.

## 2. Make a registration token

In the repository's **Settings → Runners**, click **New registration token**. The token works once, within the hour. (For all of a group's repositories, the same is on the group's page; for every repository, on the admin page.)

## 3. Register and start it

```bash
./ogit runner register --url https://git.example.com --token "$REGISTRATION_TOKEN"
./ogit runner run
```

`register` writes the runner's state to `.runner` (keep it: it holds the runner's token). `run` takes runs from the server, one at a time.

- **Labels** map `runs-on` to Docker images. The defaults map `ubuntu-latest`, `ubuntu-24.04` and `ubuntu-22.04` to Node images with git. To change them, pass `--labels` when registering, e.g. `--labels ubuntu-latest:docker://node:20-bookworm,big:docker://my/image`.
- **Networking:** job containers call the server's URL. If oGit runs in Docker on the same machine, put the jobs on its network with `./ogit runner run --network <network>`.
- Keep it running with systemd or your process manager of choice.

## 4. Add a workflow

In the repository, `.ogit/workflows/ci.yaml`:

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: echo "Hello from $GITHUB_REPOSITORY at $GITHUB_SHA"
```

Push it. The repository now has an **Actions** tab: open the run to follow its log live. Each job also posts a check on the commit, which pull requests show and [branch protection](/user/branch-protection) can require.

## Next

- [Secrets and variables](/user/actions#secrets-and-variables) for credentials.
- [Publish a container image](./publish-a-container) from a workflow.
- [Actions](/user/actions) for triggers, artifacts, caches and badges.
