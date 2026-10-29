# Publish a container image

oGit has a container registry on your server's host name. This guide pushes an image by hand, then from an Actions workflow on every tag.

## By hand

1. In **Settings → Access tokens**, make a token with `write:packages`.
2. Log in, tag and push:

   ```bash
   docker login git.example.com                      # your user name, and the token as the password
   docker build -t git.example.com/me/app:1.0 .
   docker push git.example.com/me/app:1.0
   ```

The image appears on your **Packages** tab. It's private: to share it, make it public in its settings, or link it to a repository so it follows that repository's visibility.

Docker only talks plain HTTP to `localhost` and registries listed in `insecure-registries`, so the server needs HTTPS.

## From Actions

The job's token can publish when the workflow asks for `packages: write`. The image is then linked to the repository, so the repository's developers can publish it too.

Jobs build with the runner's Docker: act gives job containers the runner's Docker socket. The job's image needs the Docker CLI, which the default Node images don't have. Map a label to an image that has it, together with Node for `actions/checkout`, when you register the runner, for example `catthehacker/ubuntu`'s act images:

```bash
./ogit runner register --url https://git.example.com --token "$REGISTRATION_TOKEN" \
  --labels ubuntu-latest:docker://node:20-bookworm,docker:docker://catthehacker/ubuntu:act-22.04
```

`.ogit/workflows/image.yaml`:

```yaml
name: Image
on:
  push:
    tags: ["v*"]
permissions:
  contents: read
  packages: write
jobs:
  image:
    runs-on: docker
    steps:
      - uses: actions/checkout@v4
      - name: Build and push
        env:
          REGISTRY: git.example.com
          TOKEN: ${{ secrets.GITHUB_TOKEN }}   # the job's token: not an environment variable unless passed like this
        run: |
          IMAGE="$REGISTRY/$GITHUB_REPOSITORY:${GITHUB_REF_NAME#v}"
          echo "$TOKEN" | docker login "$REGISTRY" -u x --password-stdin
          docker build -t "$IMAGE" .
          docker push "$IMAGE"
```

Push a tag, `git tag v1.0.0 && git push origin v1.0.0`, and the image `git.example.com/<owner>/<repo>:1.0.0` is published and linked to the repository.

Building with the runner's Docker means a job can see and use everything on that Docker host: run such runners for repositories you trust.

## Pulling

```bash
docker pull git.example.com/me/app:1.0
```

Public images pull without credentials. For private ones, log in with a token that has `read:packages`.
